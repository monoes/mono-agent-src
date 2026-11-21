package coderhost

import (
	"reflect"
	"runtime"
	"testing"
)

func TestWindowsForbiddenFolders(t *testing.T) {
	deny, siblings := windowsForbidden()
	wantDeny := []string{`C:\Windows`, `C:\Program Files`, `C:\Program Files (x86)`, `C:\ProgramData`}
	if !reflect.DeepEqual(deny, wantDeny) || !reflect.DeepEqual(siblings, []string{`C:\Users`}) {
		t.Errorf("deny=%q siblings=%q", deny, siblings)
	}
}

// The Windows folders are added on Windows only: on any other platform the list is the POSIX one, as before.
func TestDefaultForbiddenHasNoWindowsFoldersOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows folders are meant to be on the list there")
	}
	f := DefaultForbidden("/home/me", "/home/me/.monoagent")
	for _, l := range [][]string{f.Deny, f.Siblings} {
		for _, p := range l {
			if len(p) > 1 && p[1] == ':' {
				t.Errorf("a Windows folder %q is on the POSIX list", p)
			}
		}
	}
	if !reflect.DeepEqual(f.Siblings, []string{"/home", "/Users"}) {
		t.Errorf("siblings = %q", f.Siblings)
	}
}
