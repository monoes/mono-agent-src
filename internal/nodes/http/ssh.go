package httpnodes

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/monoes/mono-agent/internal/workflow"
)

// SSHNode runs a remote shell command over SSH, or (resource "file")
// uploads and downloads files over SFTP.
// Type: "http.ssh"
type SSHNode struct{}

func (n *SSHNode) Type() string { return "http.ssh" }

// CredentialDestinationKeys: the server the connection's key or password
// goes to (workflow.CheckCredentialDestinations).
func (n *SSHNode) CredentialDestinationKeys() [][]string {
	return [][]string{{"host"}, {"port"}}
}

func (n *SSHNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	host, _ := config["host"].(string)
	if host == "" {
		return nil, fmt.Errorf("http.ssh: 'host' is required")
	}

	port := 22
	if v, ok := config["port"].(float64); ok {
		port = int(v)
	}

	username, _ := config["username"].(string)
	if username == "" {
		return nil, fmt.Errorf("http.ssh: 'username' is required")
	}
	resource, _ := config["resource"].(string)
	command, _ := config["command"].(string)
	switch resource {
	case "", "command":
		if command == "" {
			return nil, fmt.Errorf("http.ssh: 'command' is required")
		}
	case "file":
		if err := checkSFTPConfig(config); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("http.ssh: unknown resource %q (want command or file)", resource)
	}

	timeoutSecs := 30
	if v, ok := config["timeout_seconds"].(float64); ok {
		timeoutSecs = int(v)
	}
	timeout := time.Duration(timeoutSecs) * time.Second

	// Connecting — resolving the host, the dial, the handshake
	// (ClientConfig.Timeout covers only ssh.Dial), opening the session and
	// its environment request — has one deadline, timeout_seconds from now,
	// and ends when the node's context does; the command then has
	// timeout_seconds of its own. The host is resolved once: whether it is
	// this machine is decided on the addresses then dialled.
	connectBy := time.Now().Add(timeout)
	resolveCtx, endResolve := context.WithDeadline(ctx, connectBy)
	target, err := resolveSSHTarget(resolveCtx, host)
	endResolve()
	if err != nil {
		return nil, fmt.Errorf("http.ssh: dial failed: %w", err)
	}
	// ssh to this machine runs a program here, as a command step would.
	if workflow.AIAuthoredFrom(ctx) && target.local {
		if err := workflow.RefuseUnapprovedProgram(ctx, "http.ssh to this computer", input.WorkflowID); err != nil {
			return nil, err
		}
	}

	// Build auth methods
	var authMethods []ssh.AuthMethod

	privateKeyPEM, _ := config["private_key"].(string)
	passphrase, _ := config["passphrase"].(string)
	password, _ := config["password"].(string)

	if privateKeyPEM != "" {
		var signer ssh.Signer
		var err error
		if passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(privateKeyPEM), []byte(passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(privateKeyPEM))
		}
		if err != nil {
			return nil, fmt.Errorf("http.ssh: failed to parse private key: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}
	if password != "" {
		authMethods = append(authMethods, ssh.Password(password))
	}
	if len(authMethods) == 0 {
		return nil, fmt.Errorf("http.ssh: either 'password' or 'private_key' is required")
	}

	// Use known_hosts file for host key verification if provided; otherwise
	// fall back to InsecureIgnoreHostKey with a clear warning.
	var hostKeyCallback ssh.HostKeyCallback
	knownHostsPath, _ := config["known_hosts"].(string)
	if knownHostsPath != "" {
		if _, err := os.Stat(knownHostsPath); err != nil {
			return nil, fmt.Errorf("http.ssh: known_hosts file not accessible: %w", err)
		}
		cb, err := knownhosts.New(knownHostsPath)
		if err != nil {
			return nil, fmt.Errorf("http.ssh: failed to parse known_hosts: %w", err)
		}
		hostKeyCallback = cb
	} else {
		log.Println("WARNING: http.ssh: host key verification is disabled (InsecureIgnoreHostKey). " +
			"Set 'known_hosts' config field to a known_hosts file path to enable verification.")
		hostKeyCallback = ssh.InsecureIgnoreHostKey() //nolint:gosec
	}

	sshConfig := &ssh.ClientConfig{
		User:            username,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         timeout,
	}

	// The host key is checked against the host as configured (addr); the
	// connection goes to an address resolveSSHTarget checked.
	addr := fmt.Sprintf("%s:%d", host, port)
	// Each address gets an equal share of what is left of the connect
	// deadline (the last one all of it), so one that never answers — a
	// broken IPv6 route ahead of a working IPv4 address — leaves time for
	// the next; the first that connects is used.
	var netConn net.Conn
	for i, a := range target.dial {
		by := connectBy
		if left := len(target.dial) - i; left > 1 {
			by = time.Now().Add(time.Until(connectBy) / time.Duration(left))
		}
		dialer := &net.Dialer{Deadline: by}
		if netConn, err = dialer.DialContext(ctx, "tcp", net.JoinHostPort(a, strconv.Itoa(port))); err == nil {
			break
		}
	}
	if err != nil {
		if ctx.Err() == nil && !time.Now().Before(connectBy) {
			return nil, fmt.Errorf("http.ssh: timed out after %s connecting (dial)", timeout)
		}
		return nil, fmt.Errorf("http.ssh: dial failed: %w", err)
	}
	_ = netConn.SetDeadline(connectBy)
	stopHandshake := context.AfterFunc(ctx, func() { _ = netConn.SetDeadline(time.Now()) })
	sshConn, chans, reqs, err := ssh.NewClientConn(netConn, addr, sshConfig)
	stopHandshake()
	if err != nil {
		netConn.Close()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("http.ssh: cancelled: %w", ctx.Err())
		}
		if !time.Now().Before(connectBy) {
			return nil, fmt.Errorf("http.ssh: timed out after %s waiting for the ssh handshake", timeout)
		}
		return nil, fmt.Errorf("http.ssh: handshake failed: %w", err)
	}
	_ = netConn.SetDeadline(time.Time{})
	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	connect := time.NewTimer(time.Until(connectBy))
	defer connect.Stop()
	if resource == "file" {
		return sftpFiles(ctx, client, connect, timeout, input, config)
	}
	// What a workflow runs is never the operator (workflow.EnvWorkflowRun),
	// over ssh too. sshd passes on only the variables AcceptEnv names, so on
	// this machine, where `monoagentcli hil approve` reaches the user's
	// database, the command exports the marker as well. Other hosts keep
	// their command as written: a Windows host's cmd.exe has no export.
	// Opening the session and the environment request end connecting in one
	// wait, so the connect timer is read once: an answer that came in time
	// is never taken for a timeout.
	var opened atomic.Bool
	session, err := awaitServer(ctx, client, connect, timeout, func() string {
		if opened.Load() {
			return "answer an environment request"
		}
		return "open a session"
	}, func() (*ssh.Session, error) {
		s, err := client.NewSession()
		if err != nil {
			return nil, err
		}
		opened.Store(true)
		_ = s.Setenv(workflow.EnvWorkflowRun, workflow.RunMarker(input.ExecutionID))
		if workflow.RunIsUnapproved(ctx) {
			_ = s.Setenv(workflow.EnvUnapprovedRun, "1")
		}
		return s, nil
	})
	if err != nil {
		return nil, err
	}
	defer session.Close()
	if exportsMarker(target) {
		command = markedCommand(input.ExecutionID, command)
		if workflow.RunIsUnapproved(ctx) {
			command = "export " + workflow.EnvUnapprovedRun + "=1; " + command
		}
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	session.Stdout = &stdoutBuf
	session.Stderr = &stderrBuf

	// Run the command in a goroutine so we can honor the workflow context and
	// the configured timeout. session.Run itself has no deadline, so without
	// this a hung remote command would block the node forever and ignore
	// cancellation.
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- session.Run(command) }()

	exitCode := 0
	var runErr error
	select {
	case runErr = <-runErrCh:
	case <-ctx.Done():
		session.Close()
		return nil, fmt.Errorf("http.ssh: cancelled: %w", ctx.Err())
	case <-timer.C:
		session.Close()
		return nil, fmt.Errorf("http.ssh: command timed out after %s", timeout)
	}
	if runErr != nil {
		if exitErr, ok := runErr.(*ssh.ExitError); ok {
			exitCode = exitErr.ExitStatus()
		} else {
			exitCode = -1
		}
	}

	resultItem := workflow.NewItem(map[string]interface{}{
		"stdout":    stdoutBuf.String(),
		"stderr":    stderrBuf.String(),
		"exit_code": exitCode,
	})

	handle := "main"
	if exitCode != 0 {
		handle = "error"
	}

	return []workflow.NodeOutput{{Handle: handle, Items: []workflow.Item{resultItem}}}, nil
}

// awaitServer runs open, a step that waits for the server, until the node's
// context ends or timer fires; then it closes client, which ends open, and
// says why (what: what the server was being waited for, at that moment).
func awaitServer[T any](ctx context.Context, client *ssh.Client, timer *time.Timer, timeout time.Duration, what func() string, open func() (T, error)) (T, error) {
	type result struct {
		v   T
		err error
	}
	done := make(chan result, 1)
	go func() {
		v, err := open()
		done <- result{v, err}
	}()
	var zero T
	select {
	case r := <-done:
		if r.err != nil {
			return zero, fmt.Errorf("http.ssh: could not %s: %w", what(), r.err)
		}
		return r.v, nil
	case <-ctx.Done():
		client.Close()
		return zero, fmt.Errorf("http.ssh: cancelled: %w", ctx.Err())
	case <-timer.C:
		client.Close()
		return zero, fmt.Errorf("http.ssh: timed out after %s waiting for the server to %s", timeout, what())
	}
}

// markedCommand is command after an export of the run's marker
// (workflow.EnvWorkflowRun), single-quoted for the remote shell.
func markedCommand(executionID, command string) string {
	marker := "'" + strings.ReplaceAll(workflow.RunMarker(executionID), "'", `'\''`) + "'"
	return "export " + workflow.EnvWorkflowRun + "=" + marker + "; " + command
}
