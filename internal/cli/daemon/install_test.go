package daemon

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/autostart"
	"github.com/monoes/mono-agent/internal/storage"
)

type recordingInstaller struct {
	installs   int
	uninstalls int
}

func (r *recordingInstaller) Install(context.Context) (autostart.Result, error) {
	r.installs++
	return autostart.Result{Description: "installed"}, nil
}
func (r *recordingInstaller) Uninstall(context.Context) error {
	r.uninstalls++
	return nil
}
func (*recordingInstaller) Status(context.Context) (bool, string) { return false, "" }
func (*recordingInstaller) Start(context.Context) error           { return nil }
func (*recordingInstaller) Restart(context.Context) error         { return nil }

// runInstall runs `daemon install` under a root that has the global --profile flag.
func runInstall(t *testing.T, rec *recordingInstaller, args ...string) (string, error) {
	t.Helper()
	return runDaemonSub(t, rec, append([]string{"install"}, args...)...)
}

// runDaemonSub runs `daemon <args>` under a root that has the global --profile flag.
func runDaemonSub(t *testing.T, rec *recordingInstaller, args ...string) (string, error) {
	t.Helper()
	root := &cobra.Command{Use: "monoagentcli"}
	root.PersistentFlags().String("profile", "", "")
	root.AddCommand(NewCommand(Deps{
		Refusal:      func() string { return "" },
		JSONOutput:   func() bool { return false },
		OpenDB:       func() (*storage.Database, error) { return storage.NewDatabase(t.TempDir() + "/t.db") },
		NewInstaller: func() autostart.Installer { return rec },
	}))
	root.SetArgs(append([]string{"daemon"}, args...))
	root.SilenceErrors, root.SilenceUsage = true, true
	var errBuf bytes.Buffer
	root.SetErr(&errBuf)
	root.SetOut(&bytes.Buffer{})
	err := root.Execute()
	return errBuf.String(), err
}

// The registered service serves every profile: install needs no --profile.
func TestInstallNeedsNoProfile(t *testing.T) {
	rec := &recordingInstaller{}
	stderr, err := runInstall(t, rec)
	if err != nil || rec.installs != 1 || stderr != "" {
		t.Fatalf("install without --profile = %v, installs %d, stderr %q", err, rec.installs, stderr)
	}
}

// A script that still passes --profile keeps working; the flag changes nothing.
func TestInstallAcceptsTheDeprecatedProfileAndSaysSo(t *testing.T) {
	rec := &recordingInstaller{}
	stderr, err := runInstall(t, rec, "--profile", "Work")
	if err != nil || rec.installs != 1 {
		t.Fatalf("install --profile Work = %v, installs %d", err, rec.installs)
	}
	if strings.Count(stderr, "\n") != 1 || !strings.Contains(stderr, "deprecated") {
		t.Fatalf("want one deprecation line, got %q", stderr)
	}
}

// uninstall goes through the injected installer, never the machine's real service manager.
func TestUninstallUsesTheInjectedInstaller(t *testing.T) {
	rec := &recordingInstaller{}
	if _, err := runDaemonSub(t, rec, "uninstall"); err != nil {
		t.Fatal(err)
	}
	if rec.uninstalls != 1 {
		t.Fatalf("uninstalls = %d, want 1 through the injected installer", rec.uninstalls)
	}
}
