package sandboxprobe

import (
	"io/fs"
	"os"
	"strings"
	"testing"
)

func in(env map[string]string, dir string, readDir func(string) ([]os.DirEntry, error), mount string) Inputs {
	i := Inputs{Getenv: func(k string) string { return env[k] }, StateDir: dir, ReadDir: readDir}
	if mount != "" {
		i.MountInfo = func() (string, error) { return mount, nil }
	}
	return i
}

func TestNothingFoundIsNotSandboxed(t *testing.T) {
	r := Detect(in(nil, "/h/.monoagent", func(string) ([]os.DirEntry, error) { return nil, fs.ErrNotExist }, "36 25 0:30 / /other rw - ext4 /dev/sda rw"))
	if r.Sandboxed || len(r.Signals) != 0 || !strings.Contains(r.Advisory, "advisory") {
		t.Fatalf("%+v", r)
	}
}

func TestEachMarkIsASignal(t *testing.T) {
	const dir = "/h/.monoagent"
	for name, c := range map[string]Inputs{
		"env marker":  in(map[string]string{EnvMarker: "bwrap"}, dir, func(string) ([]os.DirEntry, error) { return nil, fs.ErrNotExist }, ""),
		"app sandbox": in(map[string]string{"APP_SANDBOX_CONTAINER_ID": "x"}, dir, func(string) ([]os.DirEntry, error) { return nil, fs.ErrNotExist }, ""),
		"empty dir":   in(nil, dir, func(string) ([]os.DirEntry, error) { return nil, nil }, ""),
		"denied":      in(nil, dir, func(string) ([]os.DirEntry, error) { return nil, fs.ErrPermission }, ""),
		"tmpfs":       in(nil, dir, func(string) ([]os.DirEntry, error) { return nil, fs.ErrNotExist }, "40 30 0:41 / /h/.monoagent rw,nosuid - tmpfs tmpfs rw"),
	} {
		if r := Detect(c); !r.Sandboxed || len(r.Signals) != 1 {
			t.Errorf("%s: %+v", name, r)
		}
	}
}

func TestAFalsyEnvMarkerIsNoSignal(t *testing.T) {
	for _, v := range []string{"0", "false", "No", "off"} {
		r := Detect(in(map[string]string{EnvMarker: v}, "/h/.monoagent", func(string) ([]os.DirEntry, error) { return nil, fs.ErrNotExist }, ""))
		if r.Sandboxed {
			t.Errorf("%s=%q reported a sandbox: %+v", EnvMarker, v, r)
		}
	}
}

func TestATmpfsAtADirWithEscapedCharactersIsASignal(t *testing.T) {
	if !tmpfsAt(`40 30 0:41 / /h/my\040state rw - tmpfs tmpfs rw`, "/h/my state") {
		t.Error(`\040 in the mount point was not decoded`)
	}
}

func TestATmpfsElsewhereOrOtherFsIsNoSignal(t *testing.T) {
	const dir = "/h/.monoagent"
	for _, m := range []string{
		"40 30 0:41 / /tmp rw - tmpfs tmpfs rw",
		"40 30 0:41 / /h/.monoagent rw - ext4 /dev/sda rw",
	} {
		if tmpfsAt(m, dir) {
			t.Errorf("%q", m)
		}
	}
}
