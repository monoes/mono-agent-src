//go:build !linux

package chat

import (
	"errors"
	"os/exec"
)

// boundLaunch is never set off Linux: peer-credential binding needs it.
type boundLaunch struct{}

type boundDelivery struct{}

func (b *boundLaunch) attach(*exec.Cmd, string) (*boundDelivery, error) {
	return nil, errors.New("peer-credential binding needs Linux")
}
func (d *boundDelivery) start(*exec.Cmd) error {
	return errors.New("peer-credential binding needs Linux")
}
func (d *boundDelivery) kill()  {}
func (d *boundDelivery) close() {}
func (d *boundDelivery) deliver(string, string, string, int, string, func() error) error {
	return errors.New("peer-credential binding needs Linux")
}

// newBoundLaunch is nil off Linux: a launcher without one cannot launch a bound turn, and the gate refuses "on".
func newBoundLaunch(any, any, any, any, any) *boundLaunch { return nil }
