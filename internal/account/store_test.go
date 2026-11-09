package account_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
)

type brokenSealer struct{}

// describe prints a session without its access token, so a failing test never
// puts a token in its output.
func describe(s *account.Session) string {
	if s == nil {
		return "<no session>"
	}
	return fmt.Sprintf("v=%d host=%q user=%v plan=%q hw=%v attempt=%v pending=%v last=%q state=%q reason=%q token-set=%t",
		s.V, s.Host, s.User, s.Plan, s.HW, s.LastAttempt, s.PendingSince, s.LastResult, s.State, s.Reason, s.AccessToken != "")
}

func (brokenSealer) Seal([]byte) ([]byte, error) { return nil, account.ErrKeyringUnavailable }
func (brokenSealer) Open([]byte) ([]byte, error) { return nil, account.ErrKeyringUnavailable }

func newStore(t *testing.T) (account.Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "account")
	return account.OpenStore(dir, account.NewMemorySealer()), dir
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestReadsCreateNothing(t *testing.T) {
	st, dir := newStore(t)
	if sess, err := st.Load(); sess != nil || err != nil {
		t.Fatalf("Load = a session %v, %v, want none and no error", sess != nil, err)
	}
	if rt, err := st.LoadRefresh(); rt != "" || err != nil {
		t.Fatalf("LoadRefresh = a token %v, %v, want none and no error", rt != "", err)
	}
	if mt, err := st.Mtime(); !mt.IsZero() || err != nil {
		t.Fatalf("Mtime = %v, %v, want zero, nil", mt, err)
	}
	if err := st.DeleteRefresh(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a read created %s (stat err %v)", dir, err)
	}
}

func TestDefaultDirFollowsHomeAndTouchesNothing(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	got, err := account.DefaultDir()
	if want := filepath.Join(home, ".monoagent", "account"); err != nil || got != want {
		t.Fatalf("DefaultDir = %q, %v, want %q", got, err, want)
	}
	st := account.OpenStore("", nil)
	if st.Dir() != got {
		t.Fatalf("Dir = %q, want %q", st.Dir(), got)
	}
	_, _ = st.Load()
	_, _ = st.LoadRefresh()
	_, _ = st.Mtime()
	_ = st.DeleteRefresh()
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("opening and reading the default store wrote %d entries into HOME", len(entries))
	}
}

func TestStoreWithoutAHome(t *testing.T) {
	testhome.SetHome(t, "")
	st := account.OpenStore("", nil)
	if sess, err := st.Load(); sess != nil || err != nil {
		t.Fatalf("Load = a session %v, %v: with no home there is no session, not an error", sess != nil, err)
	}
	if rt, err := st.LoadRefresh(); rt != "" || err != nil {
		t.Fatalf("LoadRefresh = a token %v, %v", rt != "", err)
	}
	if err := st.Save(&account.Session{}); err == nil {
		t.Fatal("Save must fail without a home directory")
	}
	if err := st.SaveRefresh("x"); err == nil {
		t.Fatal("SaveRefresh must fail without a home directory")
	}
	if _, err := st.Lock(context.Background()); err == nil {
		t.Fatal("Lock must fail without a home directory")
	}
}

func TestSaveWritesAnAtomic0600File(t *testing.T) {
	st, dir := newStore(t)
	hw := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	in := &account.Session{
		Host: "https://monoes.me", AccessToken: "x.y.z", Plan: "free", HW: hw, LastAttempt: hw, LastResult: "ok",
		User: &account.User{ID: "u1", Email: "a@b.c", Username: "ab"},
	}
	if err := st.Save(in); err != nil {
		t.Fatal(err)
	}
	if in.V != 0 {
		t.Fatal("Save must not change the caller's session")
	}
	out, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	want := *in
	want.V = 1
	if !reflect.DeepEqual(*out, want) {
		t.Fatalf("Load = %s, want %s", describe(out), describe(&want))
	}
	if got := names(t, dir); !reflect.DeepEqual(got, []string{"session.json"}) {
		t.Fatalf("directory holds %v, want only session.json (no temporary file left)", got)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "session.json"))
	if !json.Valid(raw) || !bytes.Contains(raw, []byte("\n  \"host\"")) || raw[len(raw)-1] != '\n' {
		// The content is not printed: it holds the access token (describe's rule).
		t.Fatalf("session.json is not indented JSON with a final newline (valid JSON: %t, host key indented: %t, final newline: %t, %d bytes)",
			json.Valid(raw), bytes.Contains(raw, []byte("\n  \"host\"")), bytes.HasSuffix(raw, []byte("\n")), len(raw))
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(dir, "session.json"))
		di, _ := os.Stat(dir)
		if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
			t.Fatalf("modes: file %v, directory %v, want 0600 and 0700", fi.Mode().Perm(), di.Mode().Perm())
		}
	}
}

func TestReadersNeverSeeAPartialSession(t *testing.T) {
	st, _ := newStore(t)
	if err := st.Save(&account.Session{Host: "h", AccessToken: "t0"}); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var bad atomic.Int32
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if sess, err := st.Load(); err != nil || sess == nil {
					bad.Add(1)
				}
			}
		}()
	}
	for i := 0; i < 200; i++ {
		if err := st.Save(&account.Session{Host: "h", AccessToken: "t", LastResult: string(rune('a' + i%26))}); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	if bad.Load() != 0 {
		t.Fatalf("%d reads saw a missing or partial session while it was being replaced", bad.Load())
	}
}

func TestMtimeFollowsSaves(t *testing.T) {
	st, dir := newStore(t)
	if err := st.Save(&account.Session{Host: "h"}); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(dir, "session.json"), old, old); err != nil {
		t.Fatal(err)
	}
	if mt, _ := st.Mtime(); !mt.Equal(old) {
		t.Fatalf("Mtime = %v, want %v", mt, old)
	}
	if err := st.Save(&account.Session{Host: "h2"}); err != nil {
		t.Fatal(err)
	}
	if mt, _ := st.Mtime(); mt.Equal(old) || mt.IsZero() {
		t.Fatalf("Mtime did not change after a Save: %v", mt)
	}
}

func TestLoadRefusesAFileItCannotTrustAndASaveRepairsIt(t *testing.T) {
	st, dir := newStore(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session.json")
	for name, content := range map[string]string{
		"not JSON":        "{not json",
		"empty":           "",
		"a JSON array":    "[]",
		"no version":      `{"host":"h"}`,
		"a newer version": `{"v":2,"host":"h"}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if sess, err := st.Load(); err == nil || sess != nil {
			t.Errorf("%s: Load = a session %v, %v, want an error", name, sess != nil, err)
		}
	}
	if err := st.Save(&account.Session{Host: "h"}); err != nil {
		t.Fatal(err)
	}
	if sess, err := st.Load(); err != nil || sess == nil || sess.Host != "h" {
		t.Fatalf("a Save must repair the file: a session %v, %v", sess != nil, err)
	}
}

func TestRefreshTokenIsSealedOnDisk(t *testing.T) {
	sealer := account.NewMemorySealer()
	dir := filepath.Join(t.TempDir(), "account")
	st := account.OpenStore(dir, sealer)
	refresh := "rt-0123456789abcdef"
	if err := st.SaveRefresh(refresh); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "refresh.enc"))
	if err != nil || bytes.Contains(raw, []byte(refresh)) {
		t.Fatalf("refresh.enc missing or readable as plaintext (err %v)", err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(filepath.Join(dir, "refresh.enc")); fi.Mode().Perm() != 0o600 {
			t.Fatalf("refresh.enc mode = %v, want 0600", fi.Mode().Perm())
		}
	}
	if got, err := st.LoadRefresh(); err != nil || got != refresh {
		t.Fatalf("LoadRefresh did not return the saved refresh token (match=%v, err=%v)", got == refresh, err)
	}
	other := account.OpenStore(dir, account.NewMemorySealer())
	if _, err := other.LoadRefresh(); !errors.Is(err, account.ErrKeyringUnavailable) {
		t.Fatalf("a store with another key: err = %v, want ErrKeyringUnavailable", err)
	}
	if err := st.DeleteRefresh(); err != nil {
		t.Fatal(err)
	}
	if got, err := st.LoadRefresh(); got != "" || err != nil {
		t.Fatalf("after DeleteRefresh: a token %v, %v", got != "", err)
	}
	if err := st.DeleteRefresh(); err != nil {
		t.Fatalf("a second DeleteRefresh: %v", err)
	}
	if err := st.SaveRefresh(""); err == nil {
		t.Fatal("an empty refresh token must be refused")
	}
}

func TestAKeyStoreThatCannotBeOpened(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "account")
	broken := account.OpenStore(dir, brokenSealer{})
	if err := broken.SaveRefresh("rt-1"); !errors.Is(err, account.ErrKeyringUnavailable) {
		t.Fatalf("SaveRefresh err = %v, want ErrKeyringUnavailable", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a failed seal must not leave a directory or a file behind")
	}
	good := account.OpenStore(dir, account.NewMemorySealer())
	if err := good.SaveRefresh("rt-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := broken.LoadRefresh(); !errors.Is(err, account.ErrKeyringUnavailable) {
		t.Fatalf("LoadRefresh err = %v, want ErrKeyringUnavailable", err)
	}
}

func TestLockExcludesAndCreatesItsFileOnlyWhenTaken(t *testing.T) {
	a, dir := newStore(t)
	b := account.OpenStore(dir, account.NewMemorySealer())
	ctx := context.Background()
	unlockA, err := a.Lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(filepath.Join(dir, "session.lock")); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("session.lock: %v, %v", fi, err)
		}
	}
	short, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
	defer cancel()
	if _, err := b.Lock(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a second Lock while held: err = %v, want a deadline", err)
	}
	unlockA()
	unlockA() // safe twice
	unlockB, err := b.Lock(ctx)
	if err != nil {
		t.Fatalf("Lock after unlock: %v", err)
	}
	unlockB()
}

func TestLockWaitsForTheHolder(t *testing.T) {
	a, dir := newStore(t)
	b := account.OpenStore(dir, account.NewMemorySealer())
	unlockA, err := a.Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		unlock, err := b.Lock(ctx)
		if err != nil {
			got <- -1
			return
		}
		unlock()
		got <- time.Since(start)
	}()
	time.Sleep(150 * time.Millisecond)
	unlockA()
	if waited := <-got; waited < 100*time.Millisecond {
		t.Fatalf("the second Lock returned after %v: it did not wait for the holder (-1 means it failed)", waited)
	}
}

func TestLockSerializesGoroutines(t *testing.T) {
	_, dir := newStore(t)
	var inside, overlaps atomic.Int32
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st := account.OpenStore(dir, account.NewMemorySealer())
			for i := 0; i < 5; i++ {
				unlock, err := st.Lock(context.Background())
				if err != nil {
					overlaps.Add(100)
					return
				}
				if inside.Add(1) != 1 {
					overlaps.Add(1)
				}
				time.Sleep(time.Millisecond)
				inside.Add(-1)
				unlock()
			}
		}()
	}
	wg.Wait()
	if overlaps.Load() != 0 {
		t.Fatalf("the lock let %d holders overlap (100 or more means a Lock failed)", overlaps.Load())
	}
}
