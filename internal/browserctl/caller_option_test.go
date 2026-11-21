package browserctl_test

import (
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/browserctl/browserctltest"
	"github.com/monoes/mono-agent/internal/extension"
)

var levels = []string{browserctl.LevelFullNoApprovals, browserctl.LevelFull, browserctl.LevelAsk, browserctl.LevelDev, browserctl.LevelOff}

// tightness orders a session's policy, loosest first: no agent policy at
// all (the user's own terminal), then the levels as browserctl ranks them.
func tightness(p browserctl.Policy) int {
	if !p.Agent {
		return -1
	}
	return map[string]int{browserctl.LevelFullNoApprovals: 0, browserctl.LevelFull: 1, browserctl.LevelAsk: 2, browserctl.LevelDev: 3, browserctl.LevelOff: 4}[p.Level]
}

func clearMarkers(t *testing.T) {
	t.Helper()
	for _, k := range append(browserctl.AgentContextEnv(), browserctl.EnvAICaller) {
		t.Setenv(k, "")
	}
}

// processes are the callers a process can be: the user's terminal, an agent
// shell (other), a chat run, and a coder with its token.
var processes = map[string]func(t *testing.T){
	"user":  func(t *testing.T) {},
	"other": func(t *testing.T) { t.Setenv("CLAUDECODE", "1") },
	"chat":  func(t *testing.T) { t.Setenv(browserctl.EnvAICaller, browserctl.KindChat) },
	"coder": func(t *testing.T) {
		browserctltest.AsAgentKind(t, browserctl.KindCoder)
		t.Setenv("CLAUDECODE", "1")
	},
}

// open returns the session's policy, or the error Open gave.
func open(perms browserctl.Permissions, c *browserctl.Caller, s browserctl.Sender) (browserctl.Policy, error) {
	sess, err := browserctl.Open(browserctl.Options{NoTab: true, Permissions: &perms, Caller: c, Sender: s})
	if err != nil {
		return browserctl.Policy{}, err
	}
	return sess.Policy(), nil
}

// permsWith is the defaults with kind at level; ok false when the level is
// not allowed for the kind.
func permsWith(kind, level string) (browserctl.Permissions, bool) {
	p := browserctl.DefaultPermissions()
	return p, p.SetLevel(kind, level) == nil
}

// (a) An empty Caller changes nothing, for every kind of process.
func TestOpenEmptyCallerChangesNothing(t *testing.T) {
	for name, setup := range processes {
		t.Run(name, func(t *testing.T) {
			clearMarkers(t)
			setup(t)
			perms := browserctl.DefaultPermissions()
			want, werr := open(perms, nil, browserctltest.New())
			got, gerr := open(perms, &browserctl.Caller{}, browserctltest.New())
			if (werr == nil) != (gerr == nil) || got.Kind != want.Kind || got.Declared != want.Declared || got.Level != want.Level || got.Agent != want.Agent {
				t.Errorf("empty caller: %+v %v, without: %+v %v", got, gerr, want, werr)
			}
		})
	}
}

// (b) With an agent marker on the process, the stricter kind binds: other
// (dev) stays over a looser chat (full), and a stricter chat (off) wins.
func TestOpenStricterKindBinds(t *testing.T) {
	clearMarkers(t)
	t.Setenv("CLAUDECODE", "1")
	chat := &browserctl.Caller{Kind: browserctl.KindChat}
	if pol, err := open(browserctl.DefaultPermissions(), chat, browserctltest.New()); err != nil || pol.Kind != browserctl.KindOther || pol.Level != browserctl.LevelDev {
		t.Fatalf("chat full over an other/dev shell: %+v %v", pol, err)
	}
	perms, _ := permsWith(browserctl.KindChat, browserctl.LevelOff)
	var off *browserctl.BrowserOffError
	if _, err := open(perms, chat, browserctltest.New()); !errors.As(err, &off) {
		t.Fatalf("chat off over an other/dev shell: %v", err)
	}
	// The user's own process held as chat gets chat's level.
	clearMarkers(t)
	if pol, err := open(browserctl.DefaultPermissions(), chat, browserctltest.New()); err != nil || !pol.Agent || pol.Kind != browserctl.KindChat || pol.Level != browserctl.LevelFull {
		t.Fatalf("the user's process held as chat: %+v %v", pol, err)
	}
}

// (c) A Caller never yields a looser policy than none: every process, every
// supplied kind, every level that kind may have.
func TestOpenCallerNeverLoosens(t *testing.T) {
	for name, setup := range processes {
		t.Run(name, func(t *testing.T) {
			clearMarkers(t)
			setup(t)
			for _, kind := range browserctl.Kinds {
				for _, level := range levels {
					perms, ok := permsWith(kind, level)
					if !ok {
						continue
					}
					var offErr *browserctl.BrowserOffError
					base, berr := open(perms, nil, browserctltest.New())
					if errors.As(berr, &offErr) {
						base = browserctl.Policy{Agent: true, Level: browserctl.LevelOff}
					} else if berr != nil {
						t.Fatalf("open without a caller: %v", berr)
					}
					got, gerr := open(perms, &browserctl.Caller{Kind: kind}, browserctltest.New())
					if errors.As(gerr, &offErr) {
						continue // off: the tightest there is
					}
					if gerr != nil {
						t.Fatalf("%s at %s: %v", kind, level, gerr)
					}
					if tightness(got) < tightness(base) {
						t.Errorf("%s at %s loosened %+v to %+v", kind, level, base, got)
					}
				}
			}
		})
	}
}

// noBrowser is a bridge with no browser attached.
type noBrowser struct{ *browserctltest.Fake }

func (noBrowser) ResolveTarget(extension.Target) (extension.ConnInfo, error) {
	return extension.ConnInfo{}, extension.ErrNoExtension
}

// (d) Level off and no bound browser give the existing refusals.
func TestOpenCallerRefusals(t *testing.T) {
	clearMarkers(t)
	perms, _ := permsWith(browserctl.KindChat, browserctl.LevelOff)
	var off *browserctl.BrowserOffError
	if _, err := open(perms, &browserctl.Caller{Kind: browserctl.KindChat}, browserctltest.New()); !errors.As(err, &off) || off.Kind != browserctl.KindChat {
		t.Fatalf("chat off: %v", err)
	}
	f := browserctltest.New()
	_, err := open(browserctl.DefaultPermissions(), &browserctl.Caller{Kind: browserctl.KindChat}, noBrowser{f})
	if !errors.Is(err, extension.ErrNoExtension) || len(f.Calls()) > 0 {
		t.Fatalf("no browser: %v (calls %v)", err, f.Types())
	}
}
