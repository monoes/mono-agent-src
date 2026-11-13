//go:build windows

package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// On Windows a shell script cannot be started by the app, which execs the CLI. A fake monoagentcli is a real .exe:
// a hard link (or copy) of this test binary that, when started under the fake's name, TestMain hands to
// fakeCLIShimMain; it runs the sibling .sh script with Git for Windows' sh and exits with its status. The scripts
// stay the single source of each fake's behaviour. A machine without Git's sh fails the test that needs a fake.
const fakeCLIName = "monoagentcli.exe"

func writeFakeCLI(path, script string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.WriteFile(strings.TrimSuffix(path, ".exe")+".sh", []byte(script), 0o600); err != nil {
		return err
	}
	if os.Link(self, path) == nil {
		return nil
	}
	src, err := os.Open(self)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	return dst.Close()
}

func windowsSh() (string, error) {
	if p, err := exec.LookPath("sh"); err == nil {
		return p, nil
	}
	for _, p := range []string{`C:\Program Files\Git\usr\bin\sh.exe`, `C:\Program Files\Git\bin\sh.exe`} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errors.New("no sh found (install Git for Windows)")
}

// fakeCLIShimMain runs the fake's script when this binary was started as a fake monoagentcli.
func fakeCLIShimMain() bool {
	self, err := os.Executable()
	if err != nil || !strings.EqualFold(filepath.Base(self), fakeCLIName) {
		return false
	}
	script := strings.TrimSuffix(self, ".exe") + ".sh"
	if _, err := os.Stat(script); err != nil {
		return false
	}
	sh, err := windowsSh()
	if err != nil {
		os.Stderr.WriteString("fake monoagentcli: " + err.Error() + "\n")
		os.Exit(127)
	}
	// The arguments travel in the environment: the msys runtime of sh re-parses a command line and eats the quotes of
	// a JSON argument. The prologue rebuilds "$@" from them and sources the script.
	const prologue = `i=0; set --; while [ "$i" -lt "$SHIM_ARGC" ]; do eval "v=\$SHIM_ARG_$i"; set -- "$@" "$v"; i=$((i+1)); done; . "$SHIM_SCRIPT"`
	env := append(os.Environ(), "SHIM_SCRIPT="+filepath.ToSlash(script), "SHIM_ARGC="+strconv.Itoa(len(os.Args)-1))
	for i, a := range os.Args[1:] {
		env = append(env, "SHIM_ARG_"+strconv.Itoa(i)+"="+a)
	}
	cmd := exec.Command(sh, "-c", prologue)
	// Git's sh finds cat, sleep and friends next to itself, which a plain PATH does not hold.
	cmd.Env = append(env, "PATH="+filepath.Dir(sh)+";"+os.Getenv("PATH"))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		os.Stderr.WriteString("fake monoagentcli: " + err.Error() + "\n")
		os.Exit(127)
	}
	os.Exit(0)
	return true
}
