package stop

import (
	"crypto/hmac"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// The generation counter outside the database (note 13.5 section 5, T13): the rule `max(counter, witness,
// anchor) + 1` needs a counter that a restored database cannot lower. This one is an append-only file of
// fixed-size records (gen as 8 bytes, then a 32-byte MAC) next to the witness. A torn last record is ignored; a
// full record that does not verify is damage and entry is refused. Deleting the file does not reuse a number
// while the witness or the anchor still remember it; it is a third memory, not the only one.

const genRecLen = 8 + macLen

// ErrCounterDamaged: a complete record of the counter file does not verify.
var ErrCounterDamaged = errors.New("stop: the generation counter file is damaged; no run can be entered")

// GenCounter is the counter file of one run directory.
type GenCounter struct {
	Dir  RunDir
	Keys store.Keyer
}

func (c GenCounter) path() string { return filepath.Join(c.Dir.Dir, "generation-counter") }

func (c GenCounter) mac(gen []byte) ([]byte, error) { return c.Keys.MAC("gen-counter", gen) }

// Max returns the highest generation reserved so far (0 when there is none).
func (c GenCounter) Max() (int64, error) {
	b, err := c.Dir.fsys().ReadFile(c.path())
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var max int64
	for len(b) >= genRecLen {
		m, merr := c.mac(b[:8])
		if merr != nil || !hmac.Equal(m, b[8:genRecLen]) {
			return 0, ErrCounterDamaged
		}
		if g := int64(binary.BigEndian.Uint64(b[:8])); g > max {
			max = g
		}
		b = b[genRecLen:]
	}
	return max, nil // what remains is a torn last record
}

// WitnessMaxGen is the highest generation the witness names.
func WitnessMaxGen(recs []Record) int64 {
	var max int64
	for _, r := range recs {
		if r.Gen > max {
			max = r.Gen
		}
	}
	return max
}

// Reserve is the entry rule: max(own counter, database counter, witness, anchor) + 1, appended and fsynced
// before the database is touched, so a crash after it wastes a number and never reuses one. The caller holds the
// entry locks (host.lock, witness.lock, the database write lock).
func (c GenCounter) Reserve(dbCounter, witnessMax, anchorGen int64) (int64, error) {
	own, err := c.Max()
	if err != nil {
		return 0, err
	}
	next := own
	for _, v := range []int64{dbCounter, witnessMax, anchorGen} {
		if v > next {
			next = v
		}
	}
	next++
	rec := make([]byte, 8, genRecLen)
	binary.BigEndian.PutUint64(rec, uint64(next))
	m, err := c.mac(rec)
	if err != nil {
		return 0, err
	}
	rec = append(rec, m...)
	if _, serr := c.Dir.fsys().ReadFile(c.path()); errors.Is(serr, os.ErrNotExist) {
		err = writeDurable(c.Dir.fsys(), c.Dir.Dir, c.path(), rec)
	} else {
		err = appendDurable(c.Dir, c.path(), rec)
	}
	if err != nil {
		return 0, fmt.Errorf("stop: the generation could not be reserved: %w", err)
	}
	return next, nil
}

// NextGeneration gathers the three outside memories and reserves. A locked anchor store holds the entry; a
// missing one or no store at all contributes nothing.
func (c GenCounter) NextGeneration(dbCounter int64, w Witness, a *Anchor) (int64, error) {
	recs, _ := w.Records() // a damaged witness still names the generations it can prove
	var anchorGen int64
	if a != nil {
		st, err := a.Read()
		switch {
		case errors.Is(err, ErrStoreLocked):
			return 0, err
		case err == nil && st.Found:
			anchorGen = st.Generation
		}
	}
	return c.Reserve(dbCounter, WitnessMaxGen(recs), anchorGen)
}
