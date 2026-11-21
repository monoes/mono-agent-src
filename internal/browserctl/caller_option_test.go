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
	// A shell that declares org with no token: other, Declared org.
	"unvouched-org": func(t *testing.T) {
		t.Setenv("CLAUDECODE", "1")
		t.Setenv(browserctl.EnvAgentKind, browserctl.KindOrg)
	},
}

// suppliedCallers are the callers a node may pass, Declared ones included
// (readCaller forwards an AI caller's unproven kind, as "other/org").
var suppliedCallers = []browserctl.Caller{
	{Kind: browserctl.KindChat},
	{Kind: browserctl.KindOrg},
	{Kind: browserctl.KindOther},
	{Kind: browserctl.KindOther, Declared: browserctl.KindOrg},
	{Kind: browserctl.KindChat, Declared: browserctl.KindCoder},
	{Kind: browserctl.KindOther, Declared: browserctl.KindCoder},
}

// The unvouched shell is other with org declared, so the stricter of the two
// levels binds it: with other at full and org at dev, it is dev.
func TestOpenUnvouchedShellIsDeclared(t *testing.T) {
	clearMarkers(t)
	processes["unvouched-org"](t)
	perms := browserctl.DefaultPermissions()
	for k, l := range map[string]string{browserctl.KindOther: browserctl.LevelFull, browserctl.KindOrg: browserctl.LevelDev, browserctl.KindChat: browserctl.LevelAsk} {
		if err := perms.SetLevel(k, l); err != nil {
			t.Fatal(err)
		}
	}
	pol, err := open(perms, nil, browserctltest.New())
	if err != nil || pol.Kind != browserctl.KindOther || pol.Declared != browserctl.KindOrg || pol.Level != browserctl.LevelDev {
		t.Fatalf("unvouched org shell: %+v %v", pol, err)
	}
	// Compared by kind alone (other: full), chat's ask would look stricter
	// and win; by its binding level (dev) it must not.
	if pol, err := open(perms, &browserctl.Caller{Kind: browserctl.KindChat}, browserctltest.New()); err != nil || pol.Level != browserctl.LevelDev {
		t.Fatalf("chat at ask over the unvouched shell at dev: %+v %v", pol, err)
	}
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

// (c) A Caller never yields a looser policy than none: every process (an
// unvouched one included), every supplied caller (Declared ones included),
// every level that kind may have, and every combination of levels of the
// kinds a Declared pairs (other, org, coder, chat).
func TestOpenCallerNeverLoosens(t *testing.T) {
	var permSets []browserctl.Permissions
	for _, kind := range browserctl.Kinds {
		for _, level := range levels {
			if p, ok := permsWith(kind, level); ok {
				permSets = append(permSets, p)
			}
		}
	}
	for _, lo := range levels {
		for _, lg := range levels {
			for _, lc := range levels {
				for _, lch := range levels {
					p := browserctl.DefaultPermissions()
					if p.SetLevel(browserctl.KindOther, lo) != nil || p.SetLevel(browserctl.KindOrg, lg) != nil ||
						p.SetLevel(browserctl.KindCoder, lc) != nil || p.SetLevel(browserctl.KindChat, lch) != nil {
						continue
					}
					permSets = append(permSets, p)
				}
			}
		}
	}
	var offErr *browserctl.BrowserOffError
	for name, setup := range processes {
		t.Run(name, func(t *testing.T) {
			clearMarkers(t)
			setup(t)
			for _, perms := range permSets {
				base, berr := open(perms, nil, browserctltest.New())
				if errors.As(berr, &offErr) {
					base = browserctl.Policy{Agent: true, Level: browserctl.LevelOff}
				} else if berr != nil {
					t.Fatalf("open without a caller: %v", berr)
				}
				for _, c := range suppliedCallers {
					c := c
					got, gerr := open(perms, &c, browserctltest.New())
					if errors.As(gerr, &offErr) {
						continue // off: the tightest there is
					}
					if gerr != nil {
						t.Fatalf("%+v under %v: %v", c, perms.Levels, gerr)
					}
					if tightness(got) < tightness(base) {
						t.Fatalf("%+v under %v loosened %+v to %+v", c, perms.Levels, base, got)
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
