//go:build windows

package procbind

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The rights that start a thread in a process, write or read its memory, take a handle of it or from it, stop its threads, name it as a
// parent, and change its access list: a locked process gives none of them to anyone but SYSTEM.
var deniedProcessRights = map[string]uint32{
	"CREATE_PROCESS":  windows.PROCESS_CREATE_PROCESS,
	"CREATE_THREAD":   windows.PROCESS_CREATE_THREAD,
	"VM_WRITE":        windows.PROCESS_VM_WRITE,
	"VM_OPERATION":    windows.PROCESS_VM_OPERATION,
	"VM_READ":         windows.PROCESS_VM_READ,
	"DUP_HANDLE":      windows.PROCESS_DUP_HANDLE,
	"SUSPEND_RESUME":  0x0800,
	"SET_QUOTA":       windows.PROCESS_SET_QUOTA,
	"SET_INFORMATION": 0x0200,
	"WRITE_DAC":       windows.WRITE_DAC,
	"WRITE_OWNER":     windows.WRITE_OWNER,
	"ALL_ACCESS":      windows.PROCESS_ALL_ACCESS,
}

var keptProcessRights = map[string]uint32{
	"TERMINATE":     windows.PROCESS_TERMINATE,
	"QUERY_LIMITED": windows.PROCESS_QUERY_LIMITED_INFORMATION,
	"QUERY":         windows.PROCESS_QUERY_INFORMATION,
	"SYNCHRONIZE":   windows.SYNCHRONIZE,
	"READ_CONTROL":  windows.READ_CONTROL,
}

// withoutDebugPrivileges switches off SeDebugPrivilege and SeTakeOwnershipPrivilege of this process until the test ends. A session
// started by a service (ssh, a scheduled task) can have them enabled from the start, and a process that holds SeDebugPrivilege
// is let through every access list: the tests of the lists must not run with it, as the host does not. The state is restored.
func withoutDebugPrivileges(t *testing.T) {
	t.Helper()
	for _, name := range []string{"SeDebugPrivilege", "SeTakeOwnershipPrivilege"} {
		setPrivilege(t, name, false)
	}
}

// setPrivilege sets the state of one privilege of this process's token and puts it back when the test ends.
func setPrivilege(t *testing.T, name string, on bool) bool {
	t.Helper()
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok); err != nil {
		t.Fatal(err)
	}
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, windows.StringToUTF16Ptr(name), &luid); err != nil {
		tok.Close()
		t.Fatal(err)
	}
	attr := uint32(0)
	if on {
		attr = windows.SE_PRIVILEGE_ENABLED
	}
	next := windows.Tokenprivileges{PrivilegeCount: 1, Privileges: [1]windows.LUIDAndAttributes{{Luid: luid, Attributes: attr}}}
	var prev windows.Tokenprivileges
	var n uint32
	err := windows.AdjustTokenPrivileges(tok, false, &next, uint32(unsafe.Sizeof(prev)), &prev, &n)
	if err != nil || prev.PrivilegeCount != 1 { // not held (a process that is not elevated): nothing to switch, nothing to restore
		tok.Close()
		return false
	}
	t.Cleanup(func() {
		_ = windows.AdjustTokenPrivileges(tok, false, &prev, 0, nil, nil)
		tok.Close()
	})
	return true
}

// openAs is OpenProcess with the rights, closed at once: only whether the kernel allows it matters.
func openAs(pid int, rights uint32) error {
	h, err := windows.OpenProcess(rights, false, uint32(pid))
	if err == nil {
		windows.CloseHandle(h)
	}
	return err
}

// spawnLocked is spawn with the access list the host gives a turn's worker.
func spawnLocked(t *testing.T, mode string, env ...string) (*exec.Cmd, *bufio.Reader) {
	cmd, r, _ := spawnLockedIn(t, mode, env...)
	return cmd, r
}

// spawnLockedIn also returns the worker's standard input: the "relay" and "kid" helpers wait for a line on it.
func spawnLockedIn(t *testing.T, mode string, env ...string) (*exec.Cmd, *bufio.Reader, io.WriteCloser) {
	t.Helper()
	return spawnWith(t, mode, true, 0, env...)
}

// spawnWith starts the helper with the worker's access list (locked) or the default one, under tok when it is not zero.
func spawnWith(t *testing.T, mode string, locked bool, tok syscall.Token, env ...string) (*exec.Cmd, *bufio.Reader, io.WriteCloser) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), append([]string{helperEnv + "=" + mode}, env...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Token: tok}
	if locked {
		pa, ta, err := WorkerAttributes()
		if err != nil || pa == nil || ta == nil {
			t.Fatalf("no access list for the worker (%v)", err)
		}
		cmd.SysProcAttr.ProcessAttributes, cmd.SysProcAttr.ThreadAttributes = pa, ta
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd, bufio.NewReader(out), in
}

func isDenied(err error) bool { return errors.Is(err, windows.ERROR_ACCESS_DENIED) }

// Control: without the access list the worker can be named as a parent and the child is served as a descendant. This is what the
// access list closes; if it ever stops being possible the tests below prove nothing.
func TestControlAWorkerWithTheDefaultAccessListCanBeClaimedAsAParent(t *testing.T) {
	withoutDebugPrivileges(t)
	cmd, r := spawn(t, "sleep")
	readLine(t, r, "READY")
	other, err := OpenProcessHandle(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	s := startService(t, other, nil)
	if v, _ := s.verify(); v != VerdictDeny {
		t.Fatalf("control: this process is not in the worker's tree: %v", v)
	}
	for name, rights := range deniedProcessRights {
		if name == "WRITE_OWNER" || name == "ALL_ACCESS" {
			continue
		}
		if err := openAs(cmd.Process.Pid, rights); err != nil {
			t.Fatalf("control: a default process cannot be opened with %s (%v): the control is void", name, err)
		}
	}
	ph, err := windows.OpenProcess(windows.PROCESS_CREATE_PROCESS|windows.PROCESS_DUP_HANDLE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(ph)
	if err := rawClaim(ph); err != nil {
		t.Fatalf("control: CreateProcess with the process named as its parent failed: %v", err)
	}
	if got := claimAsParent(t, s, ph); got != "VERDICT allow" {
		t.Fatalf("control: the claim did not pass the walk (%q)", got)
	}
}

// claimAsParent starts the verifying helper with the process behind ph as its named parent, and returns what it printed (or the
// error that started nothing).
func claimAsParent(t *testing.T, s *svc, ph windows.Handle) string {
	t.Helper()
	c := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	c.Env = append(os.Environ(), helperEnv+"=verify", "PB_ROOT="+s.root, "PB_KEY="+hex.EncodeToString(s.key))
	c.SysProcAttr = &syscall.SysProcAttr{ParentProcess: syscall.Handle(ph)}
	out, err := c.Output()
	if err != nil {
		return "ERR " + err.Error()
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(first)
}

// A same-user process cannot name the locked worker as its parent: the right is not given, and a handle that lacks it makes
// CreateProcess fail with access denied, so no child that the walk would take for a descendant exists.
func TestLockedWorkerCannotBeClaimedAsAParent(t *testing.T) {
	withoutDebugPrivileges(t)
	cmd, r := spawnLocked(t, "sleep")
	readLine(t, r, "READY")
	if other, err := OpenProcessHandle(cmd.Process.Pid); err != nil {
		t.Fatalf("the service cannot pin the locked worker: %v", err)
	} else {
		other.Close()
	}
	// the right to name it as a parent, alone and with what a start by Go's own exec asks for besides
	for _, rights := range []uint32{windows.PROCESS_CREATE_PROCESS, windows.PROCESS_CREATE_PROCESS | windows.PROCESS_DUP_HANDLE | windows.PROCESS_QUERY_LIMITED_INFORMATION} {
		if err := openAs(cmd.Process.Pid, rights); !isDenied(err) {
			t.Fatalf("the locked worker was opened with %#x for naming it as a parent: %v", rights, err)
		}
	}
	// the handle a same-user process does get (everything the list gives) is not enough to name the worker as a parent
	ph, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(ph)
	if err := rawClaim(ph); !isDenied(err) {
		t.Fatalf("CreateProcess with the locked worker named as its parent: %v, want access denied", err)
	}
}

// rawClaim is CreateProcess with PROC_THREAD_ATTRIBUTE_PARENT_PROCESS = ph and nothing else (no handle is duplicated into the
// parent, which Go's own start does and which needs another right): it fails with the kernel's answer to the parent claim alone.
func rawClaim(ph windows.Handle) error {
	al, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return err
	}
	defer al.Delete()
	if err := al.Update(windows.PROC_THREAD_ATTRIBUTE_PARENT_PROCESS, unsafe.Pointer(&ph), unsafe.Sizeof(ph)); err != nil {
		return err
	}
	si := windows.StartupInfoEx{ProcThreadAttributeList: al.List()}
	si.StartupInfo.Cb = uint32(unsafe.Sizeof(si))
	var pi windows.ProcessInformation
	cmdline, _ := windows.UTF16PtrFromString(os.Args[0] + " -test.run=^$")
	if err := windows.CreateProcess(nil, cmdline, nil, nil, false, windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_NO_WINDOW, nil, nil, &si.StartupInfo, &pi); err != nil {
		return err
	}
	windows.CloseHandle(pi.Thread)
	windows.CloseHandle(pi.Process)
	return nil
}

// Writing into, starting a thread in, duplicating from, suspending and re-listing the locked worker are all refused; what the
// host, the service and the owner need is not.
func TestLockedWorkerRefusesInjectionAndKeepsWhatTheHostNeeds(t *testing.T) {
	withoutDebugPrivileges(t)
	cmd, r := spawnLocked(t, "sleep")
	readLine(t, r, "READY")
	for name, rights := range deniedProcessRights {
		if err := openAs(cmd.Process.Pid, rights); !isDenied(err) {
			t.Errorf("the locked worker opened with %s: %v", name, err)
		}
	}
	for name, rights := range keptProcessRights {
		if err := openAs(cmd.Process.Pid, rights); err != nil {
			t.Errorf("the locked worker cannot be opened with %s: %v", name, err)
		}
	}
	// the classic route as a whole: a remote thread needs a handle with CREATE_THREAD, which no one gets
	ph, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(ph)
	k := windows.NewLazySystemDLL("kernel32.dll").NewProc("CreateRemoteThread")
	if h, _, e := k.Call(uintptr(ph), 0, 0, 0, 0, 0, 0); h != 0 {
		windows.CloseHandle(windows.Handle(h))
		t.Fatalf("a thread was started in the locked worker")
	} else if !errors.Is(e, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("CreateRemoteThread on the locked worker: %v", e)
	}
	// the access list cannot be rewritten by the owner either
	err = windows.SetSecurityInfo(ph, windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("the access list of the locked worker was rewritten by the owner")
	}
}

// The legitimate path: the host pins the worker it started (with the right to end it), the walk and the user are read through the
// locked list, a tool that the worker starts is served, and the worker is found by os.FindProcess (the host liveness probe) and
// stopped by handle.
func TestLockedWorkerIsStillServedPinnedAndStopped(t *testing.T) {
	withoutDebugPrivileges(t)
	cmd, r, in := spawnLockedIn(t, "relay")
	pid := cmd.Process.Pid
	w, err := OpenWorkerHandle(pid)
	if err != nil {
		t.Fatalf("the host cannot pin the worker it started: %v", err)
	}
	defer w.Close()
	own, _ := OwnSID()
	if sid, err := w.UserSID(); err != nil || sid != own {
		t.Fatalf("the worker's user %q (%v), want %q", sid, err, own)
	}
	if p, err := (WinProcs{}).Parent(w); err != nil || p != os.Getpid() {
		t.Fatalf("the worker's parent %d %v", p, err)
	}
	if !w.Alive() {
		t.Fatal("the pinned worker is not alive")
	}
	s := startService(t, w, nil)
	// the tool is started by the locked worker with the default list, like any tool a worker starts
	io.WriteString(in, "PB_ROOT="+s.root+" PB_KEY="+hex.EncodeToString(s.key)+"\n")
	if got := readLine(t, r, "VERDICT "); got != "allow" {
		t.Fatalf("a tool started by the locked worker: %q", got)
	}
	if p, err := os.FindProcess(pid); err != nil {
		t.Fatalf("os.FindProcess on the locked worker: %v", err)
	} else {
		p.Release()
	}
	if err := w.Terminate(); err != nil {
		t.Fatalf("the locked worker cannot be ended by the host's handle: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for w.Alive() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if w.Alive() {
		t.Fatal("the worker is still alive after Terminate")
	}
}

// taskkill is what `coder host stop` uses for a turn; it needs nothing but the right to end the process.
func TestLockedWorkerIsStoppedByTaskkill(t *testing.T) {
	withoutDebugPrivileges(t)
	cmd, r := spawnLocked(t, "sleep")
	readLine(t, r, "READY")
	if out, err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).CombinedOutput(); err != nil {
		t.Fatalf("taskkill on the locked worker: %v %s", err, out)
	}
	done := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the locked worker survived taskkill")
	}
}

// The host process itself is locked the same way, and can still be found, queried and ended by the rest of the system.
func TestLockSelfLocksTheProcessAndKeepsItReadable(t *testing.T) {
	withoutDebugPrivileges(t)
	cmd, r := spawn(t, "locksself")
	readLine(t, r, "LOCKED")
	for name, rights := range deniedProcessRights {
		if err := openAs(cmd.Process.Pid, rights); !isDenied(err) {
			t.Errorf("the locked host opened with %s: %v", name, err)
		}
	}
	for name, rights := range keptProcessRights {
		if err := openAs(cmd.Process.Pid, rights); err != nil {
			t.Errorf("the locked host cannot be opened with %s: %v", name, err)
		}
	}
	if err := cmd.Process.Kill(); err != nil { // the handle of its starter
		t.Fatal(err)
	}
}

// A same-user process that is NOT elevated (the common attacker) is refused by the locked list and not by the default one.
func TestANonElevatedSameUserProcessCannotOpenALockedWorker(t *testing.T) {
	withoutDebugPrivileges(t)
	// the worker and the attacker are both ordinary processes of the user: the same (medium) integrity level, as in real use. An
	// elevated host would put the worker above a medium attacker, and the integrity level alone would then refuse it.
	tok := restrictedMediumToken(t)
	defer tok.Close()
	locked, lr, _ := spawnWith(t, "sleep", true, syscall.Token(tok))
	readLine(t, lr, "READY")
	plain, pr, _ := spawnWith(t, "sleep", false, syscall.Token(tok))
	readLine(t, pr, "READY")
	c := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	c.Env = append(os.Environ(), helperEnv+"=attack", "PB_LOCKED="+strconv.Itoa(locked.Process.Pid), "PB_PLAIN="+strconv.Itoa(plain.Process.Pid))
	c.SysProcAttr = &syscall.SysProcAttr{Token: syscall.Token(tok)}
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("the non-elevated attacker could not run: %v %s", err, out)
	}
	res := map[string]string{}
	for _, l := range strings.Split(string(out), "\n") {
		if f := strings.Fields(l); len(f) == 3 && f[0] == "OPEN" {
			res[f[1]] = f[2]
		}
	}
	if !strings.Contains(string(out), "ELEVATED false") {
		t.Fatalf("the attacker is elevated, the test proves nothing about a normal user: %s", out)
	}
	for name := range deniedProcessRights {
		if name == "WRITE_OWNER" || name == "ALL_ACCESS" {
			continue
		}
		if res["plain:"+name] != "ok" {
			t.Errorf("control: the non-elevated attacker cannot open a default process with %s (%s)", name, res["plain:"+name])
		}
		if res["locked:"+name] != "denied" {
			t.Errorf("the non-elevated attacker opened the locked worker with %s (%s)", name, res["locked:"+name])
		}
	}
}

func attackFromHelper() {
	tok := windows.GetCurrentProcessToken()
	fmt.Println("ELEVATED", tok.IsElevated())
	names := make([]string, 0, len(deniedProcessRights))
	for n := range deniedProcessRights {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, which := range []string{"LOCKED", "PLAIN"} {
		pid, _ := strconv.Atoi(os.Getenv("PB_" + which))
		for _, n := range names {
			state := "ok"
			if err := openAs(pid, deniedProcessRights[n]); err != nil {
				state = "other"
				if isDenied(err) {
					state = "denied"
				}
			}
			fmt.Println("OPEN", strings.ToLower(which)+":"+n, state)
		}
	}
}

// restrictedMediumToken is the token of an ordinary, non-elevated process of this user: the same user, no administrator
// rights, no privileges (so no SeDebugPrivilege), and the medium integrity level.
func restrictedMediumToken(t *testing.T) windows.Token {
	t.Helper()
	var src windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_ADJUST_DEFAULT, &src); err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	const disableMaxPrivilege, luaToken = 0x1, 0x4
	var out windows.Token
	r, _, e := windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken").Call(uintptr(src), disableMaxPrivilege|luaToken, 0, 0, 0, 0, 0, 0, uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		t.Fatalf("CreateRestrictedToken: %v", e)
	}
	medium, err := windows.StringToSid("S-1-16-8192")
	if err != nil {
		t.Fatal(err)
	}
	l := windows.Tokenmandatorylabel{Label: windows.SIDAndAttributes{Sid: medium, Attributes: windows.SE_GROUP_INTEGRITY}}
	if err := windows.SetTokenInformation(out, windows.TokenIntegrityLevel, (*byte)(unsafe.Pointer(&l)), uint32(unsafe.Sizeof(l))+windows.GetLengthSid(medium)); err != nil {
		out.Close()
		t.Fatalf("lowering the integrity level: %v", err)
	}
	// the default access list of a standard user's token (the user and SYSTEM everything): the token of an elevated process
	// carries Administrators instead, which a filtered token cannot use, and the processes made under it would then have
	// nothing for their own user
	user, err := out.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	system, _ := windows.StringToSid("S-1-5-18")
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		{AccessPermissions: windows.GENERIC_ALL, AccessMode: windows.GRANT_ACCESS, Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid)}},
		{AccessPermissions: windows.GENERIC_ALL, AccessMode: windows.GRANT_ACCESS, Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeValue: windows.TrusteeValueFromSID(system)}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	dd := struct{ Acl *windows.ACL }{acl}
	if err := windows.SetTokenInformation(out, windows.TokenDefaultDacl, (*byte)(unsafe.Pointer(&dd)), uint32(unsafe.Sizeof(dd))); err != nil {
		out.Close()
		t.Fatalf("default access list of the token: %v", err)
	}
	return out
}
