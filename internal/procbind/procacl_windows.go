//go:build windows

package procbind

import (
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WorkerAttributes are the security attributes a turn's worker is created with: the process gets the locked access list of
// procacl.go, and so does its main thread (a thread handle with the right to set its context is an injection route that needs no
// right on the process). The host's own handle to the child is full-access whatever the list says, so the host keeps what it needs.
func WorkerAttributes() (proc, thread *syscall.SecurityAttributes, err error) {
	workerOnce.Do(func() {
		sid, err := OwnSID()
		if err != nil {
			workerErr = fmt.Errorf("procbind: the access list of the worker needs the user: %w", err)
			return
		}
		if workerProc, workerErr = attributesOf(processDescriptor(sid)); workerErr != nil {
			return
		}
		workerThread, workerErr = attributesOf(threadDescriptor(sid))
	})
	if workerErr != nil {
		return nil, nil, workerErr
	}
	return workerProc, workerThread, nil
}

// the attributes are built once: the user does not change, and the descriptors they point to (as numbers, which the collector
// does not follow) are kept in keep for the life of the process
var (
	workerOnce               sync.Once
	workerProc, workerThread *syscall.SecurityAttributes
	workerErr                error
	keep                     []*windows.SECURITY_DESCRIPTOR
)

func attributesOf(sddl string) (*syscall.SecurityAttributes, error) {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, fmt.Errorf("procbind: access list %q: %w", sddl, err)
	}
	keep = append(keep, sd)
	return &syscall.SecurityAttributes{Length: uint32(unsafe.Sizeof(syscall.SecurityAttributes{})), SecurityDescriptor: uintptr(unsafe.Pointer(sd))}, nil
}

// LockSelf gives this process the locked access list of procacl.go (the host process: it holds the registry of the turns'
// workers, and a process of the same user that wrote into it would hold them). Threads the process makes keep the default list,
// see SECURITY.md. It is safe to call twice.
func LockSelf() error {
	sid, err := OwnSID()
	if err != nil {
		return fmt.Errorf("procbind: the access list of this process needs the user: %w", err)
	}
	sd, err := windows.SecurityDescriptorFromString(processDescriptor(sid))
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("procbind: the access list of this process: %v", err)
	}
	return windows.SetSecurityInfo(windows.CurrentProcess(), windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
