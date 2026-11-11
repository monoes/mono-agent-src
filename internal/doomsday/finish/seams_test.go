package finish

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// S7c1a seam: a copy and a digest stop when their context ends (a cancelled check used to leak a goroutine and its folder until
// the copy ended on its own).
func TestDigestTreeCtxStopsAtOnceWhenTheContextIsDone(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a", "b", "c"} {
		write(t, root, n+"/f.txt", n, 0o644)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DigestTreeCtx(ctx, root, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled digest returned %v, want context.Canceled", err)
	}
	live, err := DigestTreeCtx(context.Background(), root, "")
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := DigestTree(root, ""); err != nil || plain.Digest != live.Digest {
		t.Fatalf("the ctx digest differs from the plain one: %v", err)
	}
}

// The walk checks the context on its own (a tree of folders has no file to read).
func TestDigestTreeCtxWalkStopsWithoutAnyFileToRead(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"a/b", "c/d/e"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DigestTreeCtx(ctx, root, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}

func TestSnapshotCtxCancelledLeavesNothingBehind(t *testing.T) {
	root := t.TempDir()
	write(t, root, "big.bin", string(make([]byte, 1<<20)), 0o644)
	dst := filepath.Join(t.TempDir(), "snap")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := SnapshotCtx(ctx, root, dst); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled snapshot returned %v", err)
	}
	if _, err := os.Lstat(dst); !os.IsNotExist(err) {
		t.Fatalf("a cancelled snapshot left %s behind: %v", dst, err)
	}
	// and a live context copies exactly as Snapshot does
	snap, err := SnapshotCtx(context.Background(), root, dst)
	t.Cleanup(func() { RemoveTree(dst) })
	if err != nil {
		t.Fatal(err)
	}
	if live, _ := DigestTree(root, ""); live.Digest != snap.Digest {
		t.Fatal("the ctx snapshot digests differently")
	}
}

// S7c1a seam: a confiner that rewrites the sandbox's command needs the program and the limits the sandbox put behind the helper.
func TestUnwrapLimitsHelperReadsWhatHelperArgvWrote(t *testing.T) {
	l := Limits{CPUSeconds: 7, AddressSpace: 5 << 30, OpenFiles: 33, FileSize: 4096, Procs: 3001}
	argv := helperArgv("/exe", l, "/usr/bin/sh", []string{"-c", "echo hi"})
	got, prog, args, ok := UnwrapLimitsHelper(argv)
	if !ok || got != l || prog != "/usr/bin/sh" || !reflect.DeepEqual(args, []string{"-c", "echo hi"}) {
		t.Fatalf("unwrap = %+v %q %q %v", got, prog, args, ok)
	}
	for name, bad := range map[string][]string{
		"empty":         nil,
		"no marker":     {"/exe", "/usr/bin/sh", "-c", "x"},
		"no separator":  {"/exe", limitsMarker, "1,2,3,4,5", "/usr/bin/sh"},
		"bad spec":      {"/exe", limitsMarker, "1,2,3", "--", "/usr/bin/sh"},
		"zero limit":    {"/exe", limitsMarker, "0,2,3,4,5", "--", "/usr/bin/sh"},
		"no program":    {"/exe", limitsMarker, "1,2,3,4,5", "--"},
		"relative prog": {"/exe", limitsMarker, "1,2,3,4,5", "--", "sh"},
	} {
		if _, _, _, ok := UnwrapLimitsHelper(bad); ok {
			t.Errorf("%s: unwrapped", name)
		}
	}
}

// The copy itself looks at the context between chunks (not only the walk between entries).
func TestACopyStopsInsideAFileWhenTheContextEnds(t *testing.T) {
	root := t.TempDir()
	write(t, root, "big.bin", string(make([]byte, 1<<20)), 0o644)
	st, err := os.Lstat(filepath.Join(root, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dst := t.TempDir()
	if _, err := sumFile(ctx, filepath.Join(root, "big.bin"), dst, "big.bin", false, st); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled copy returned %v", err)
	}
	if sum, err := sumFile(context.Background(), filepath.Join(root, "big.bin"), "", "big.bin", false, st); err != nil || sum == "" {
		t.Fatalf("%v", err)
	}
}
