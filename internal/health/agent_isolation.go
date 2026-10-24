package health

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// CheckAgentIsolation reports what an agent-mode boundary has to stand on, on this machine.
// It is read-only and offline: it looks for the sandbox program, asks the kernel whether
// unprivileged user namespaces are available, and on Windows lists the access control list of
// the state folder for the agent's account. Without an account to look at it never warns or fails.
const CheckAgentIsolation = "core.agent_isolation"

// AgentIsolationDoc is the command that prints the Windows recipe.
const AgentIsolationDoc = "monoagentcli agent-mode windows"

// AgentAccountEnv names the agent's Windows account when --agent-account is not given.
const AgentAccountEnv = "MONOAGENT_AGENT_ACCOUNT"

// isolationProbe is what the check looks at; tests replace it.
type isolationProbe struct {
	goos     string
	lookPath func(name string) (string, error)
	readFile func(path string) ([]byte, error)
	// run starts a program and returns its combined output.
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

var agentIsolationProbe = isolationProbe{
	goos:     runtime.GOOS,
	lookPath: exec.LookPath,
	readFile: os.ReadFile,
	run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	},
}

func checkAgentIsolation(ctx context.Context, env *Env) Result {
	p := agentIsolationProbe
	switch p.goos {
	case "linux":
		return linuxIsolation(ctx, p)
	case "darwin":
		return darwinIsolation(p)
	case "windows":
		return windowsIsolation(ctx, p, env)
	}
	return Result{Status: StatusInfo, Summary: "agent mode has no sandbox profile for " + p.goos}
}

func darwinIsolation(p isolationProbe) Result {
	if path, err := p.lookPath("sandbox-exec"); err == nil {
		return Result{Status: StatusInfo, Summary: "sandbox-exec is present (" + path + "); `agent-mode sandbox --runtime seatbelt` prints a profile for it"}
	}
	return Result{Status: StatusInfo, Summary: "sandbox-exec was not found, so the seatbelt agent-mode profile cannot be used here"}
}

func linuxIsolation(ctx context.Context, p isolationProbe) Result {
	var facts []string
	bwrap, err := p.lookPath("bwrap")
	have := err == nil
	if have {
		facts = append(facts, "bwrap is present ("+bwrap+")")
	} else {
		facts = append(facts, "bwrap was not found (install bubblewrap to use the bwrap agent-mode profiles)")
	}
	ns, why := userNamespaces(p)
	if have {
		// A real start of the sandbox program answers more than the kernel switches do.
		if _, err := p.run(ctx, bwrap, "--unshare-user", "--ro-bind", "/", "/", "true"); err == nil {
			ns, why = true, "bwrap started a process in a new user namespace"
		} else {
			ns, why = false, "bwrap could not start a process in a new user namespace: "+err.Error()
		}
	}
	if ns {
		facts = append(facts, "unprivileged user namespaces work ("+why+")")
	} else {
		facts = append(facts, "unprivileged user namespaces do not work here ("+why+")")
	}
	return Result{Status: StatusInfo, Summary: strings.Join(facts, "; ")}
}

// userNamespaces reads the kernel switches that decide whether an unprivileged process may
// create a user namespace. Missing switches mean the kernel does not have them.
func userNamespaces(p isolationProbe) (bool, string) {
	val := func(path string) (int, bool) {
		b, err := p.readFile(path)
		if err != nil {
			return 0, false
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(b)))
		return n, err == nil
	}
	if n, ok := val("/proc/sys/user/max_user_namespaces"); ok && n == 0 {
		return false, "user.max_user_namespaces is 0"
	}
	if n, ok := val("/proc/sys/kernel/unprivileged_userns_clone"); ok && n == 0 {
		return false, "kernel.unprivileged_userns_clone is 0"
	}
	if n, ok := val("/proc/sys/kernel/apparmor_restrict_unprivileged_userns"); ok && n == 1 {
		return false, "kernel.apparmor_restrict_unprivileged_userns is 1"
	}
	return true, "no kernel switch forbids them"
}

func windowsIsolation(ctx context.Context, p isolationProbe, env *Env) Result {
	account := strings.TrimSpace(env.AgentAccount)
	if account == "" {
		return Result{Status: StatusInfo,
			Summary: "not configured: Windows has no agent sandbox; the agent runs as a separate low-privilege account (--agent-account NAME checks its access to " + env.DataDir + "). Run `" + AgentIsolationDoc + "` for the steps"}
	}
	out, err := p.run(ctx, "icacls", env.DataDir)
	return judgeACL(account, env.DataDir, string(out), err)
}

// judgeACL turns icacls output for dir into a verdict about account.
func judgeACL(account, dir, out string, runErr error) Result {
	if runErr != nil {
		return Result{Status: StatusWarn, Summary: "could not read the access list of " + dir + " with icacls, so " + account + " is unchecked",
			Detail: strings.TrimSpace(runErr.Error() + "\n" + out)}
	}
	aces := parseICACLS(out, dir)
	if len(aces) == 0 {
		return Result{Status: StatusWarn, Summary: "icacls listed no entries for " + dir + ", so " + account + " is unchecked"}
	}
	level, via := aclLevelFor(account, aces)
	caveat := "Only entries naming the account or a group every local account is in (Everyone, Users, Authenticated Users, by English name or SID) " +
		"are counted. Membership of other groups, and group names in another language, are not visible in icacls output: check them by hand. " +
		"Deny entries are not evaluated."
	switch level {
	case aclWrite:
		return Result{Status: StatusFail, Summary: account + " can change " + dir + " (" + via + ")", Detail: caveat}
	case aclRead:
		return Result{Status: StatusWarn, Summary: account + " can read " + dir + " (" + via + ")", Detail: caveat}
	}
	return Result{Status: StatusOK, Summary: "no entry on " + dir + " gives " + account + " access", Detail: caveat}
}

type aclLevel int

const (
	aclNone aclLevel = iota
	aclRead
	aclWrite
)

type ace struct {
	principal string
	deny      bool
	level     aclLevel
}

// aceLine matches "PRINCIPAL:(flag)(flag)..." at the end of a line.
var aceLine = regexp.MustCompile(`^(.+?):((?:\([^)]*\))+)\s*$`)

// Rights icacls prints for what a process may change; inheritance flags carry no right.
var (
	aclInherit = map[string]bool{"I": true, "OI": true, "CI": true, "IO": true, "NP": true}
	aclWrites  = map[string]bool{"F": true, "M": true, "W": true, "D": true, "DC": true, "WD": true, "AD": true, "WA": true,
		"WEA": true, "WDAC": true, "WO": true, "GA": true, "GW": true}
)

// parseICACLS reads the entries of icacls output. The first entry shares its line with the
// path; the closing summary line has no ":(" in it in any language, so nothing in it matches.
func parseICACLS(out, dir string) []ace {
	var aces []ace
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if len(line) >= len(dir) && strings.EqualFold(line[:len(dir)], dir) {
			line = strings.TrimSpace(line[len(dir):])
		}
		m := aceLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		a := ace{principal: strings.TrimSpace(m[1])}
		for _, grp := range strings.Split(strings.Trim(m[2], "()"), ")(") {
			for _, tok := range strings.Split(grp, ",") {
				tok = strings.ToUpper(strings.TrimSpace(tok))
				switch {
				case tok == "", aclInherit[tok]:
				case tok == "DENY":
					a.deny = true
				case tok == "N":
				case aclWrites[tok]:
					a.level = aclWrite
				case a.level < aclRead:
					a.level = aclRead
				}
			}
		}
		aces = append(aces, a)
	}
	return aces
}

// everyAccount are the principals of which every local account is a member.
var everyAccount = map[string]bool{
	"everyone": true, "builtin\\users": true, "nt authority\\authenticated users": true,
	"*s-1-1-0": true, "*s-1-5-11": true, "*s-1-5-32-545": true,
}

func aclLevelFor(account string, aces []ace) (aclLevel, string) {
	best, via := aclNone, ""
	for _, a := range aces {
		if a.deny || a.level == aclNone {
			continue
		}
		var who string
		switch {
		case samePrincipal(account, a.principal):
			who = "entry for " + a.principal
		case everyAccount[strings.ToLower(a.principal)]:
			who = "entry for " + a.principal + ", a group the account belongs to"
		default:
			continue
		}
		if a.level > best {
			best, via = a.level, who
		}
	}
	return best, via
}

// samePrincipal compares an account as given (NAME, .\NAME or HOST\NAME) with an icacls principal.
func samePrincipal(account, principal string) bool {
	an, ad := splitAccount(account)
	pn, pd := splitAccount(principal)
	if !strings.EqualFold(an, pn) {
		return false
	}
	return ad == "" || ad == "." || strings.EqualFold(ad, pd)
}

func splitAccount(s string) (name, domain string) {
	if i := strings.LastIndex(s, "\\"); i >= 0 {
		return s[i+1:], s[:i]
	}
	return s, ""
}
