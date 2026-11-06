//go:build windows

package stop

import (
	"context"
	"errors"
)

// SystemHost is the Windows UnitHost: there is no service manager; the Containment creates the unit itself. Its only
// job is the convenience stop (end the job by name), never the proof that a unit is gone.
func SystemHost() UnitHost { return jobHost{os: realJobOS()} }

type jobHost struct{ os JobOS }

func (jobHost) StartUnit(context.Context, string, string) error {
	return errors.New("stop: a Windows unit is created by its Containment, not by a service manager")
}

func (h jobHost) StopUnit(_ context.Context, name string) error {
	j, err := h.os.OpenJob(jobName(name))
	if errors.Is(err, ErrJobAbsent) {
		return nil
	}
	if err != nil {
		return err
	}
	defer j.Close()
	return j.Terminate()
}

// WindowsKernel is the Kernel of a Windows host. Only the pieces a job unit uses answer; every cgroup operation
// refuses, so a cgroup record is never proven here.
type WindowsKernel struct{}

func (WindowsKernel) BootID() (string, error)        { return realJobOS().BootID() }
func (WindowsKernel) CurrentUID() uint32             { return 0 }
func (WindowsKernel) DelegatedRoot() (string, error) { return "", errors.New("no cgroups on Windows") }
func (WindowsKernel) SelfCgroup() (string, error)    { return "", errors.New("no cgroups on Windows") }
func (WindowsKernel) OpenCgroup(string) (Cgroup, error) {
	return nil, errors.New("no cgroups on Windows")
}
func (WindowsKernel) IsAbsent(error) bool                       { return false }
func (WindowsKernel) ManagerControlGroup(string) (string, bool) { return "", false }
func (WindowsKernel) ListUnitNames(string) ([]string, error)    { return nil, nil }
