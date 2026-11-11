package stop

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// The witness (note 13.5 section 4A): an append-only file in the run directory, independent of the database.
// Framing: SYNC | len | payload | MAC, MAC over len||payload keyed by the vault-derived Keyer, the payload
// carrying wprev (the MAC of the previous witness record). A torn tail needs no repair: the reader resyncs on
// the next SYNC and accepts a record only if it is good and chaining.

const (
	syncLen = 8
	macLen  = 32
	magic   = "MAWH1\x00\x00\x00"
)

// Record types.
const (
	RecHeader    = "header"
	RecDBID      = "dbid"
	RecPending   = "pending"
	RecCommitted = "committed"
	RecAbandoned = "abandoned"
	RecFailed    = "failed"
	// RecAnchor says the generation is anchor-bound (the OS secret store holds its item). Transparent to Classify.
	RecAnchor = "anchor"
	// The unit lifecycle lines of note 13.2 section 4; transparent to Classify. Extra carries the fields.
	RecUnitIntent = "unit-intent"
	RecUnitBound  = "unit-bound"
	RecUnitEnded  = "unit-ended"
	// RecUnitLeader is one session leader of a session unit (sessunit.go), written before its turn is released; Extra is
	// its JSON. Transparent to Classify like the other unit lines.
	RecUnitLeader = "unit-leader"
)

// Record is the payload of one witness record.
type Record struct {
	T      string `json:"t"`
	Gen    int64  `json:"gen"`
	From   int64  `json:"from,omitempty"`   // first seq of a pending range
	To     int64  `json:"to,omitempty"`     // last seq of a pending range / the committed seq
	WPrev  string `json:"wprev"`            // MAC of the previous witness record
	DBHead string `json:"dbhead,omitempty"` // database head MAC the writer read under the lock
	Head   string `json:"head,omitempty"`   // expected (pending) or final (committed) head MAC
	Ending bool   `json:"ending,omitempty"` // the transition is stop-requested or later
	Ended  bool   `json:"ended,omitempty"`  // the generation ended
	Row    int    `json:"row,omitempty"`    // failed: the row number that fired
	Extra  string `json:"extra,omitempty"`  // dbid: the database identity; pending: the kind
	mac    []byte
}

// Witness reads and appends the witness file.
type Witness struct {
	Dir  RunDir
	Keys store.Keyer
	Hook Hook
}

func (w Witness) path() string     { return filepath.Join(w.Dir.Dir, "witness") }
func (w Witness) lockPath() string { return filepath.Join(w.Dir.Dir, "witness.lock") }

func (w Witness) mac(parts ...[]byte) ([]byte, error) { return w.Keys.MAC("witness", parts...) }

// ErrWitnessDamaged: missing, unreadable, replayed, forked or deleted records (row 7).
var ErrWitnessDamaged = errors.New("stop: the witness is damaged or unreadable")

// Init creates the file with its header (random SYNC) create-exclusive.
func (w Witness) Init() error {
	sync := make([]byte, syncLen)
	if _, err := rand.Read(sync); err != nil {
		return err
	}
	m, err := w.mac([]byte("header"), sync)
	if err != nil {
		return err
	}
	data := append(append([]byte(magic), sync...), m...)
	return writeDurable(w.Dir.fsys(), w.Dir.Dir, w.path(), data)
}

// scan parses the file: the good chaining records, whether damage inside the chain was found, and the MAC of
// the last good record (the header's, when there is none).
func (w Witness) scan() (recs []Record, last []byte, damaged bool, err error) {
	b, rerr := w.Dir.fsys().ReadFile(w.path())
	if rerr != nil || len(b) < len(magic)+syncLen+macLen || !bytes.HasPrefix(b, []byte(magic)) {
		return nil, nil, true, ErrWitnessDamaged
	}
	sync := b[len(magic) : len(magic)+syncLen]
	hm, err := w.mac([]byte("header"), sync)
	if err != nil || !hmac.Equal(hm, b[len(magic)+syncLen:len(magic)+syncLen+macLen]) {
		return nil, nil, true, ErrWitnessDamaged
	}
	last = hm
	pos := len(magic) + syncLen + macLen
	lastSeq := map[int64]int64{} // seqs are per generation
	for pos < len(b) {
		i := bytes.Index(b[pos:], sync)
		if i < 0 {
			break // only a torn fragment remains
		}
		pos += i
		rec, n, ok := w.parseAt(b, pos)
		if !ok {
			pos++ // a fragment or a fake SYNC: skip in turn
			continue
		}
		if rec.WPrev != hex.EncodeToString(last) {
			// authenticated but not chaining: a replay, a fork or a deletion; never silently skipped
			return recs, last, true, ErrWitnessDamaged
		}
		// An abandoned record names the seq the stream went back to (the seq before the abandoned range), so it is
		// the one record whose To may be lower than a range it supersedes.
		if rec.To != 0 && rec.T != RecAbandoned && rec.To < lastSeq[rec.Gen] {
			return recs, last, true, ErrWitnessDamaged
		}
		if rec.To > lastSeq[rec.Gen] {
			lastSeq[rec.Gen] = rec.To
		}
		recs = append(recs, rec)
		last = rec.mac
		pos += n
	}
	return recs, last, false, nil
}

func (w Witness) parseAt(b []byte, pos int) (Record, int, bool) {
	var rec Record
	hdr := pos + syncLen
	if hdr+4 > len(b) {
		return rec, 0, false
	}
	ln := int(binary.BigEndian.Uint32(b[hdr : hdr+4]))
	end := hdr + 4 + ln + macLen
	if ln <= 0 || ln > 1<<16 || end > len(b) {
		return rec, 0, false
	}
	body := b[hdr : hdr+4+ln]
	m, err := w.mac(body)
	if err != nil || !hmac.Equal(m, b[hdr+4+ln:end]) {
		return rec, 0, false
	}
	if json.Unmarshal(b[hdr+4:hdr+4+ln], &rec) != nil {
		return rec, 0, false
	}
	rec.mac = m
	return rec, end - pos, true
}

// Records returns the good chaining records, or ErrWitnessDamaged.
func (w Witness) Records() ([]Record, error) {
	recs, _, damaged, err := w.scan()
	if damaged {
		return recs, ErrWitnessDamaged
	}
	return recs, err
}

// Append adds one record under the witness lock (one critical section for every writer), fsynced. The caller
// holds LockWitness for the whole sequencer section; Append itself only chains and writes.
func (w Witness) Append(r Record) error {
	_, last, damaged, err := w.scan()
	if damaged || err != nil {
		return ErrWitnessDamaged
	}
	b, rerr := w.Dir.fsys().ReadFile(w.path())
	if rerr != nil {
		return rerr
	}
	sync := b[len(magic) : len(magic)+syncLen]
	r.WPrev = hex.EncodeToString(last)
	payload, err := json.Marshal(r)
	if err != nil {
		return err
	}
	body := make([]byte, 4, 4+len(payload))
	binary.BigEndian.PutUint32(body, uint32(len(payload)))
	body = append(body, payload...)
	m, err := w.mac(body)
	if err != nil {
		return err
	}
	frame := append(append(append([]byte{}, sync...), body...), m...)
	return appendDurable(w.Dir, w.path(), frame)
}

func appendDurable(r RunDir, path string, data []byte) error {
	if a, ok := r.fsys().(appender); ok {
		return a.AppendSync(path, data, r.Dir)
	}
	return fmt.Errorf("stop: the file system seam cannot append")
}

// appender is the append capability of the FS seam (OSFS implements it).
type appender interface {
	AppendSync(path string, data []byte, dir string) error
}
