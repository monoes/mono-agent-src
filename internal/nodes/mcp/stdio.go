package mcpnode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/nodemgr"
	"github.com/monoes/mono-agent/internal/proctree"
	"github.com/monoes/mono-agent/internal/workflow"
)

// exitGrace is how long a server gets to exit on its own after its stdin
// closes before its process group is killed.
const exitGrace = 500 * time.Millisecond

// stdioTransport talks to a server it spawned, newline-delimited JSON-RPC
// over the child's stdin and stdout.
type stdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *tailBuffer

	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[string]chan *rpcMessage

	readDone chan struct{} // closed when stdout ends
	readErr  error
	waitDone chan struct{} // closed when the child has been waited for

	closeOnce sync.Once
}

// startStdio spawns command in its own process group (so close reaches
// anything it starts too) and begins reading its stdout.
func startStdio(command string, args []string, env map[string]string, cwd string) (*stdioTransport, error) {
	// Like system.execute_command: the user's own PATH, so `npx`/`uvx`
	// resolve to what the user's shell would run.
	bin := command
	if p, err := nodemgr.LookPathUser(command); err == nil {
		bin = p
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = workflow.WithoutRunToken(nodemgr.UserEnv(os.Environ())) // the engine process's own run token is never handed on
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if cwd != "" {
		cmd.Dir = cwd
	}
	setProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	t := &stdioTransport{
		cmd:      cmd,
		stdin:    stdin,
		stderr:   &tailBuffer{max: 8 << 10},
		pending:  map[string]chan *rpcMessage{},
		readDone: make(chan struct{}),
		waitDone: make(chan struct{}),
	}
	cmd.Stderr = t.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start MCP server %q: %w", command, err)
	}
	go func() {
		_ = cmd.Wait()
		close(t.waitDone)
	}()
	go t.readLoop(stdout)
	return t, nil
}

func (t *stdioTransport) readLoop(r io.Reader) {
	defer close(t.readDone)
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := readLine(br)
		if len(bytes.TrimSpace(line)) > 0 {
			var m rpcMessage
			if jerr := json.Unmarshal(line, &m); jerr == nil {
				t.dispatch(&m)
			}
			// Anything that is not JSON-RPC (a stray log line) is skipped.
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = errors.New("MCP server closed its output")
			}
			t.readErr = err
			return
		}
	}
}

// readLine reads one line, refusing lines over maxMessageBytes.
func readLine(br *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if len(buf)+len(chunk) > maxMessageBytes {
			return nil, fmt.Errorf("MCP server sent a message over %d MB", maxMessageBytes>>20)
		}
		buf = append(buf, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return buf, err
	}
}

func (t *stdioTransport) dispatch(m *rpcMessage) {
	switch {
	case m.isRequest():
		_ = t.write(replyToServer(m))
	case m.isResponse():
		key := idKey(m.ID)
		t.mu.Lock()
		ch := t.pending[key]
		delete(t.pending, key)
		t.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	}
	// Notifications (logging, progress, list_changed) are ignored.
}

func (t *stdioTransport) write(b []byte) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	_, err := t.stdin.Write(append(b, '\n'))
	return err
}

func (t *stdioTransport) request(ctx context.Context, id string, msg []byte) (*rpcMessage, error) {
	ch := make(chan *rpcMessage, 1)
	t.mu.Lock()
	t.pending[id] = ch
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
	}()
	if err := t.write(msg); err != nil {
		return nil, t.failure(fmt.Errorf("write to MCP server: %w", err))
	}
	select {
	case m := <-ch:
		return m, nil
	case <-t.readDone:
		// A response read just before stdout ended still counts.
		select {
		case m := <-ch:
			return m, nil
		default:
		}
		return nil, t.failure(t.readErr)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (t *stdioTransport) notify(_ context.Context, msg []byte) error {
	if err := t.write(msg); err != nil {
		return t.failure(fmt.Errorf("write to MCP server: %w", err))
	}
	return nil
}

// failure adds the server's last stderr output to err.
func (t *stdioTransport) failure(err error) error {
	if tail := strings.TrimSpace(t.stderr.String()); tail != "" {
		return fmt.Errorf("%w (server stderr: %s)", err, tail)
	}
	return err
}

// close closes the server's stdin, gives it exitGrace to exit, then kills
// its whole process group and waits for it.
func (t *stdioTransport) close() {
	t.closeOnce.Do(func() {
		_ = t.stdin.Close()
		select {
		case <-t.waitDone:
		case <-time.After(exitGrace):
		}
		// Also reaches anything the server started in its group.
		proctree.Kill(t.cmd)
		<-t.waitDone
	})
}

// idKey normalises a JSON-RPC id so 1 and "1" match the request key.
func idKey(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if len(s) >= 2 && s[0] == '"' {
		var str string
		if json.Unmarshal([]byte(s), &str) == nil {
			return str
		}
	}
	return s
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.max; over > 0 {
		b.buf = append([]byte(nil), b.buf[over:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
