package coderhost

import "strings"

// The Windows half of "private to this user" for the turn registry. Go reports 0777 for every Windows
// directory and 0666 for files, so the mode bits cannot say who may write; the owner and the DACL do. The
// decision is plain data in, bool out, so it is tested on every platform with a fake ACL; only reading the
// ACL (turn_registry_owner_windows.go) is Windows-only.

type aceKind int

const (
	aceAllow   aceKind = iota // ACCESS_ALLOWED_ACE
	aceDeny                   // ACCESS_DENIED_ACE (only ever restricts: ignored)
	aceIgnored                // audit and alarm ACEs: they grant nothing
	aceUnknown                // any other type (object, callback, ...): not understood, so not trusted
)

// aclEntry is one ACE of a DACL: its kind, the trustee's SID in string form and its access mask.
type aclEntry struct {
	Kind aceKind
	SID  string
	Mask uint32
}

const (
	sidSystem        = "S-1-5-18"
	sidAdministrator = "S-1-5-32-544"
	sidCreatorOwner  = "S-1-3-0"

	// the rights that let a trustee change, add or remove entries of a directory or alter a file
	aceWriteRights uint32 = 0x00000002 | // FILE_WRITE_DATA / FILE_ADD_FILE
		0x00000004 | // FILE_APPEND_DATA / FILE_ADD_SUBDIRECTORY
		0x00000010 | // FILE_WRITE_EA
		0x00000040 | // FILE_DELETE_CHILD
		0x00000100 | // FILE_WRITE_ATTRIBUTES
		0x00010000 | // DELETE
		0x00040000 | // WRITE_DAC
		0x00080000 | // WRITE_OWNER
		0x10000000 | // GENERIC_ALL
		0x40000000 // GENERIC_WRITE
)

// privateACL says whether an object is private to the user whose SID is user: it is owned by user (or by
// ownerToo, the token's default owner, which differs for an elevated process), it has a DACL, and no ACE
// grants a write right to anyone but user, SYSTEM, Administrators and CREATOR OWNER. Deny ACEs are ignored
// (they only restrict). Anything not understood is refused: it fails closed.
func privateACL(owner string, hasDACL bool, aces []aclEntry, user, ownerToo string) bool {
	if user == "" || owner == "" || !hasDACL {
		return false // no DACL grants everyone full access
	}
	if !strings.EqualFold(owner, user) && (ownerToo == "" || !strings.EqualFold(owner, ownerToo)) {
		return false
	}
	for _, a := range aces {
		switch a.Kind {
		case aceDeny, aceIgnored:
			continue
		case aceAllow:
			if a.Mask&aceWriteRights == 0 {
				continue
			}
			if a.SID == "" {
				return false
			}
			switch {
			case strings.EqualFold(a.SID, user), strings.EqualFold(a.SID, ownerToo),
				a.SID == sidSystem, a.SID == sidAdministrator, a.SID == sidCreatorOwner:
			default:
				return false
			}
		default:
			return false
		}
	}
	return true
}

// registryDirSDDL is the security descriptor a registry directory is created with: protected (it inherits
// nothing from the parent) and full control for the user and SYSTEM only; children inherit it.
func registryDirSDDL(userSID string) string {
	return "D:PAI(A;OICI;FA;;;" + userSID + ")(A;OICI;FA;;;SY)"
}
