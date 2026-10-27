//go:build wfcrash && !windows

package workflow

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
)

// pausedOnce makes a pause fire the first time a point is reached in this process only, so a change
// that is retried does not stop at the same point again.
var pausedOnce = &onceSet{seen: map[string]bool{}}

type onceSet struct {
	mu   sync.Mutex
	seen map[string]bool
}

// swap records name and reports whether it had been seen before.
func (o *onceSet) swap(name string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	was := o.seen[name]
	o.seen[name] = true
	return was
}

// failpoint acts when the environment names this point (test builds only, tag wfcrash):
//
//	MONOAGENT_WF_FAILPOINT=<name>            the process kills itself with SIGKILL, so no deferred
//	                                         cleanup runs
//	MONOAGENT_WF_FAILPOINT_ERR=<name>        the point returns an error
//	MONOAGENT_WF_FAILPOINT_PAUSE=<name>:<p>  the process writes <p>.reached and blocks until
//	                                         something is written to the fifo <p>
func failpoint(name string) error {
	if os.Getenv("MONOAGENT_WF_FAILPOINT") == name {
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		select {}
	}
	if os.Getenv("MONOAGENT_WF_FAILPOINT_ERR") == name {
		return fmt.Errorf("failpoint %s", name)
	}
	if v := os.Getenv("MONOAGENT_WF_FAILPOINT_PAUSE"); strings.HasPrefix(v, name+":") && !pausedOnce.swap(name) {
		path := strings.TrimPrefix(v, name+":")
		_ = os.WriteFile(path+".reached", []byte(name), 0o600)
		if f, err := os.Open(path); err == nil {
			_, _ = f.Read(make([]byte, 1))
			f.Close()
		}
	}
	return nil
}
