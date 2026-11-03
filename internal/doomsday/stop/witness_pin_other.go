//go:build !linux

package stop

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrPinUnsupported: the binding is proven on Linux only; elsewhere the mode does not start.
var (
	ErrPinUnsupported = errors.New("stop: the pinned database binding is available on Linux only; the mode does not start here")
	ErrPinSwapped     = errors.New("stop: a file of the database was swapped")
	ErrPinBroken      = errors.New("stop: the pinned connection was discarded")
	ErrNotClean       = errors.New("stop: the pinned connection did not close cleanly")
)

// FileID is the identity of a file.
type FileID struct{ Dev, Ino uint64 }

func (f FileID) String() string { return fmt.Sprintf("%x:%x", f.Dev, f.Ino) }

// PinOptions configures Pin.
type PinOptions struct {
	DBPath string
	Open   func(path string) (*sql.DB, error)
	Hook   Hook
}

// Pinned is never constructed off Linux.
type Pinned struct{}

func Pin(context.Context, PinOptions) (*Pinned, error) { return nil, ErrPinUnsupported }

func (*Pinned) Identity() FileID { return FileID{} }
func (*Pinned) Check() error     { return ErrPinUnsupported }
func (*Pinned) CloseClean() error {
	return ErrPinUnsupported
}
func (*Pinned) Do(context.Context, func(*sql.Conn, func() error) error) error {
	return ErrPinUnsupported
}
