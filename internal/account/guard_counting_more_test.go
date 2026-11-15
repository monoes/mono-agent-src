package account_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
)

// countingStore is a Store that counts the calls made to it, and can fail a stat
// or run something between a read and its return, so that a test sees how often
// and in what order the guard touches the disk, not only what it answers.
type countingStore struct {
	account.Store

	mu        sync.Mutex
	counts    map[string]int
	mtimeErr  error  // Mtime fails with it while it is set
	loadErr   error  // Load fails with it while it is set, as a disk that cannot be read does (failLoad)
	afterLoad func() // runs once Load has read the file, before it returns
}

// failLoad makes every Load fail with err, an error that says nothing about what
// session.json holds (permission denied, an I/O error), until it is called with nil.
func (s *countingStore) failLoad(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadErr = err
}

func newCountingStore(s account.Store) *countingStore {
	return &countingStore{Store: s, counts: map[string]int{}}
}

func (s *countingStore) count(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts[name]++
}

func (s *countingStore) calls(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[name]
}

func (s *countingStore) Mtime() (time.Time, error) {
	s.count("Mtime")
	s.mu.Lock()
	err := s.mtimeErr
	s.mu.Unlock()
	if err != nil {
		return time.Time{}, err
	}
	return s.Store.Mtime()
}

func (s *countingStore) Load() (*account.Session, error) {
	s.count("Load")
	s.mu.Lock()
	lerr := s.loadErr
	s.mu.Unlock()
	if lerr != nil {
		return nil, lerr
	}
	sess, err := s.Store.Load()
	s.mu.Lock()
	after := s.afterLoad
	s.mu.Unlock()
	if after != nil {
		after()
	}
	return sess, err
}

func (s *countingStore) Save(sess *account.Session) error {
	s.count("Save")
	return s.Store.Save(sess)
}

func (s *countingStore) LoadRefresh() (string, error) {
	s.count("LoadRefresh")
	return s.Store.LoadRefresh()
}

func (s *countingStore) SaveRefresh(token string) error {
	s.count("SaveRefresh")
	return s.Store.SaveRefresh(token)
}

func (s *countingStore) DeleteRefresh() error {
	s.count("DeleteRefresh")
	return s.Store.DeleteRefresh()
}

func (s *countingStore) Lock(ctx context.Context) (func(), error) {
	s.count("Lock")
	return s.Store.Lock(ctx)
}

// expectReads fails the test unless the guard has stat-ed the session file
// stats times and read it reads times so far.
func (s *countingStore) expectReads(t *testing.T, when string, stats, reads int) {
	t.Helper()
	if m, l := s.calls("Mtime"), s.calls("Load"); m != stats || l != reads {
		t.Fatalf("%s: %d stats and %d reads so far, want %d and %d", when, m, l, stats, reads)
	}
}

// guardOver returns a guard over s, refreshing through the fake monoes.me, on the
// fixture clock. poll 0 is the default PollInterval.
func (e *env) guardOver(s account.Store, poll time.Duration) *account.Guard {
	e.t.Helper()
	g := account.NewGuard(account.GuardOptions{Store: s, Refresher: e.ref, Now: e.f.Clock.Now, Poll: poll})
	e.t.Cleanup(g.Close)
	return g
}

// corrupt makes session.json text that is not JSON, as a full disk or a hand
// edit would, and returns the file's modification time.
func (e *env) corrupt() time.Time {
	e.t.Helper()
	if err := os.MkdirAll(e.dir, 0o700); err != nil {
		e.t.Fatal(err)
	}
	path := filepath.Join(e.dir, "session.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		e.t.Fatal(err)
	}
	e.touch()
	fi, err := os.Stat(path)
	if err != nil {
		e.t.Fatal(err)
	}
	return fi.ModTime()
}

// refuse stores a session that monoes.me refused, as another process that was
// refused would have.
func (e *env) refuse() {
	e.t.Helper()
	e.save(&account.Session{V: 1, Host: account.HostURL, User: &account.User{ID: "user-1"}, State: "refused"})
}

// storeToken stores a session around any access token, verified or not.
func (e *env) storeToken(token string, hw time.Time) {
	e.t.Helper()
	e.save(&account.Session{V: 1, Host: account.HostURL, AccessToken: token, User: &account.User{ID: "user-1"}, HW: hw})
}

// quietFor is how long a test waits to see that a goroutine does NOT act: 150
// ms, above the 100 ms floor for a test that waits on real time. A wait that is
// too short can let a wrong guard pass, never fail a right one.
const quietFor = 150 * time.Millisecond

// quiet gives a goroutine that should not act a real moment to do so.
func quiet(t testing.TB, gs ...*account.Guard) {
	t.Helper()
	settle(t, gs...)
}

// describeStatus prints a status with its user's ID in place of a pointer.
func describeStatus(st account.Status) string {
	id := ""
	if st.User != nil {
		id = st.User.ID
	}
	return fmt.Sprintf("%s/%q user=%q plan=%q issued=%v valid=%v grace=%v enforced=%t",
		st.State, st.Reason, id, st.Plan, st.IssuedAt, st.ValidUntil, st.GraceUntil, st.Enforced)
}

func mustMtime(t *testing.T, s account.Store) time.Time {
	t.Helper()
	mt, err := s.Mtime()
	if err != nil {
		t.Fatal(err)
	}
	return mt
}
