//go:build windows

package coderhost

import (
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Go reports 0777 for every Windows directory and 0666 for files, so the mode bits say nothing about who may
// write; the owner and the DACL do (see privateACL). Every failure to read them is "not private".

// currentSIDs are the SID of the user running this process and the token's default owner (the same SID,
// except for an elevated process, whose new objects are owned by Administrators).
func currentSIDs() (user, owner string, err error) {
	tok := windows.GetCurrentProcessToken()
	u, err := tok.GetTokenUser()
	if err != nil {
		return "", "", err
	}
	var n uint32
	if err := windows.GetTokenInformation(tok, windows.TokenOwner, nil, 0, &n); err != windows.ERROR_INSUFFICIENT_BUFFER {
		return "", "", err
	}
	buf := make([]byte, n)
	if err := windows.GetTokenInformation(tok, windows.TokenOwner, &buf[0], n, &n); err != nil {
		return "", "", err
	}
	o := (*struct{ Owner *windows.SID })(unsafe.Pointer(&buf[0]))
	return u.User.Sid.String(), o.Owner.String(), nil
}

// readACL returns the owner and the DACL entries of path. hasDACL is false for a NULL DACL.
func readACL(path string) (owner string, hasDACL bool, aces []aclEntry, err error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return "", false, nil, err
	}
	o, _, err := sd.Owner()
	if err != nil || o == nil {
		return "", false, nil, err
	}
	owner = o.String()
	acl, _, err := sd.DACL()
	if err != nil {
		return "", false, nil, err
	}
	if acl == nil {
		return owner, false, nil, nil
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return "", false, nil, err
		}
		e := aclEntry{Mask: uint32(ace.Mask)}
		switch ace.Header.AceType {
		case windows.ACCESS_ALLOWED_ACE_TYPE, windows.ACCESS_DENIED_ACE_TYPE:
			e.SID = (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
			if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
				e.Kind = aceDeny
			}
		case 2, 3: // SYSTEM_AUDIT, SYSTEM_ALARM
			e.Kind = aceIgnored
		default:
			e.Kind = aceUnknown
		}
		aces = append(aces, e)
	}
	return owner, true, aces, nil
}

// privateToUs: the object at path is owned by this user and no one else may write to it. loose (mode bits) is
// for the other platforms.
func privateToUs(path string, _ os.FileInfo, _ os.FileMode) bool {
	user, ownerToo, err := currentSIDs()
	if err != nil {
		return false
	}
	owner, hasDACL, aces, err := readACL(path)
	if err != nil {
		return false
	}
	return privateACL(owner, hasDACL, aces, user, ownerToo)
}

// makeRegistryDir creates dir for this user alone: the parents with the usual inheritance, dir itself with a
// protected DACL naming the user and SYSTEM (registryDirSDDL). An existing dir is left as it is, for the check.
func makeRegistryDir(dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	if _, err := os.Lstat(dir); err == nil {
		return nil
	}
	user, _, err := currentSIDs()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(registryDirSDDL(user))
	if err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	if err := windows.CreateDirectory(p, sa); err != nil && !os.IsExist(err) {
		return err
	}
	return nil
}
