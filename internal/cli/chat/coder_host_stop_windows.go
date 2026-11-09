//go:build windows

package chat

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func stopHostOS(cfg *Deps, info hostInfo) (bool, error) {
	return stopPinnedHost(winHostStopOps(hostStopGrace), info, filepath.Join(hostDir(cfg), hostInfoFile))
}

func winHostStopOps(grace time.Duration) hostStopOps {
	return hostStopOps{open: openWinHostPin, grace: grace, killWait: 10 * time.Second}
}

// winHostPin is the handle opened once on the host's process.
type winHostPin struct {
	h     windows.Handle
	stamp string
}

func openWinHostPin(pid int) (hostPin, error) {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) { // no process has this id
			return nil, errHostGone
		}
		return nil, err
	}
	var created, exited, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(syscall.Handle(h), &created, &exited, &kernel, &user); err != nil {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("reading the host's start time: %w", err)
	}
	return &winHostPin{h: h, stamp: "created:" + strconv.FormatInt(created.Nanoseconds(), 10)}, nil
}

func (p *winHostPin) Stamp() string { return p.stamp }

func (p *winHostPin) Signal(instance string) error {
	name := hostStopEventName(instance)
	if name == "" {
		return errors.New("host.json names no usable instance")
	}
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	ev, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, n)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(ev)
	return windows.SetEvent(ev)
}

func (p *winHostPin) WaitExit(d time.Duration) bool {
	ms := uint32(d / time.Millisecond)
	if d > 0 && ms == 0 {
		ms = 1
	}
	ev, err := windows.WaitForSingleObject(p.h, ms)
	return err == nil && ev == windows.WAIT_OBJECT_0
}

func (p *winHostPin) Terminate() error { return windows.TerminateProcess(p.h, 1) }

func (p *winHostPin) Close() { windows.CloseHandle(p.h) }

// watchHostStop creates the event `host stop` sets (named for this instance, usable by the current user only) and calls
// stop when it is set, so the host leaves through the same path as on SIGTERM and its deferred cleanup runs. The returned
// function ends the watch. An event that already exists is refused: the host made it, nobody else.
func watchHostStop(ctx context.Context, instance string, stop func()) (func(), error) {
	name := hostStopEventName(instance)
	if name == "" {
		return nil, errors.New("no usable instance name")
	}
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + tu.User.Sid.String() + ")")
	if err != nil {
		return nil, err
	}
	sa := &windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	ev, err := windows.CreateEvent(sa, 1, 0, n)
	if err != nil {
		if ev != 0 {
			windows.CloseHandle(ev)
		}
		return nil, err
	}
	wctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for wctx.Err() == nil {
			if r, werr := windows.WaitForSingleObject(ev, 200); werr == nil && r == windows.WAIT_OBJECT_0 {
				stop()
				return
			}
		}
	}()
	return func() { cancel(); wg.Wait(); windows.CloseHandle(ev) }, nil
}
