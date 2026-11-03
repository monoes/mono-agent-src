//go:build !linux

package stop

import "os/exec"

// TicketCgroup does not exist off Linux: the mode does not start there (probe), so nothing is ever placed.
type TicketCgroup struct{}

// OpenTicketCgroup refuses off Linux.
func OpenTicketCgroup(Cgroup, uint64) (*TicketCgroup, error) { return nil, ErrContainmentUnavailable }

// OpenSubCgroup refuses off Linux.
func OpenSubCgroup(Cgroup, string) (*TicketCgroup, error) { return nil, ErrContainmentUnavailable }

// Name is empty off Linux.
func (*TicketCgroup) Name() string { return "" }

// Apply does nothing off Linux.
func (*TicketCgroup) Apply(*exec.Cmd) {}

// Close does nothing off Linux.
func (*TicketCgroup) Close() error { return nil }
