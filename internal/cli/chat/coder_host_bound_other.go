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
