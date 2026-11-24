package control

import (
	"bytes"
	"errors"
)

// maxPythonOutputBytes caps what a Python script may print on each of stdout and stderr: the output
// is held in memory whole, so a script that prints without end would otherwise fill it.
const maxPythonOutputBytes = 64 << 20

var errPythonOutputTooLarge = errors.New("output is larger than the limit")

// cappedBuffer is a bytes.Buffer that refuses a write that would take it past max. The refusal ends
// the process's copy of that stream (the process gets a broken pipe) and marks the buffer exceeded.
type cappedBuffer struct {
	buf      bytes.Buffer
	max      int
	exceeded bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.max {
		b.exceeded = true
		return 0, errPythonOutputTooLarge
	}
	return b.buf.Write(p)
}

func (b *cappedBuffer) Bytes() []byte  { return b.buf.Bytes() }
func (b *cappedBuffer) String() string { return b.buf.String() }
