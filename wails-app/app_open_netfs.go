package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Best-effort detection of network locations, used only on the click-time
// lookup of a chat path (see the note above chatEvalSymlinks). The platform
// files supply the real probes; the matching below is pure so it is tested
// everywhere.

// chatOnNetworkFS reports whether an already resolved path lives on a network
// filesystem or a remote drive. Tests replace it.
var chatOnNetworkFS = onNetworkFilesystem

// chatLinkGuard runs before the symlink lookup of a clicked path. Tests
// replace it.
var chatLinkGuard = windowsLinkGuard

// Seams for the link walk.
var (
	chatLstat    = os.Lstat
	chatReadlink = os.Readlink
)

// Network detection is BEST EFFORT. It is the statfs type (or the drive type)
// of the RESOLVED path, so an overlayfs (whatever its lower layer is: an NFS
// lower layer reports overlay), a FUSE mount (sshfs, rclone) and any other
// filesystem that hides what backs it are NOT detected. The check also runs
// after the path was resolved, so it prevents USE of a network result
// (preview, reveal) and not the contact itself; only the Windows link walk
// (walkChatLinks) tries to prevent contact.

// linuxNetworkFSMagic are statfs f_type values of network filesystems: NFS,
// SMB, CIFS, SMB2, AFS, kAFS, Ceph, 9p, Coda and NCP. FUSE (0x65735546) is
// deliberately not listed: it is also sshfs, rclone and many local
// filesystems, and a FUSE mount is not recognised as a network one.
var linuxNetworkFSMagic = map[uint32]bool{
	0x6969: true, 0x517B: true, 0xFF534D42: true, 0xFE534D42: true,
	0x5346414F: true, 0x6B414653: true, 0x00C36400: true, 0x01021997: true,
	0x73757245: true, 0x564C: true,
}

func isLinuxNetworkFSMagic(t uint32) bool { return linuxNetworkFSMagic[t] }

// isDarwinNetworkFSName: f_fstypename values of network filesystems.
func isDarwinNetworkFSName(name string) bool {
	switch strings.ToLower(name) {
	case "nfs", "smbfs", "afpfs", "webdav":
		return true
	}
	return false
}

// driveRemote is the Windows GetDriveType result for a network drive.
const driveRemote = 4

func isRemoteDriveType(t uint32) bool { return t == driveRemote }

// vcomponents splits p after its volume name into its non-empty components.
func vcomponents(p string) (vol string, parts []string) {
	vol = filepath.VolumeName(p)
	rest := p[len(vol):]
	return vol, strings.FieldsFunc(rest, func(r rune) bool { return r == '/' || r == '\\' })
}

// linkLeavesVolume: the text of a link names a UNC share, a device path or a
// drive other than vol (the volume the path starts on).
func linkLeavesVolume(target, vol string) bool {
	if len(target) >= 2 && (target[0] == '/' || target[0] == '\\') && (target[1] == '/' || target[1] == '\\') {
		return true
	}
	if len(target) >= 2 && target[1] == ':' && ((target[0]|0x20) >= 'a' && (target[0]|0x20) <= 'z') {
		return !strings.EqualFold(target[:2], vol)
	}
	return false
}

// walkChatLinks reads each component of p with Lstat (no link is followed)
// and refuses a symlink, junction or other reparse point whose own text
// (Readlink also follows nothing) names a UNC share or another drive. A
// component that does not exist ends the walk: the lookup will report it.
func walkChatLinks(p string, lstat func(string) (os.FileInfo, error), readlink func(string) (string, error)) error {
	vol, parts := vcomponents(p)
	cur := vol + string(filepath.Separator)
	for _, c := range parts {
		cur = filepath.Join(cur, c)
		fi, err := lstat(cur)
		if err != nil {
			return nil
		}
		if fi.Mode()&(os.ModeSymlink|os.ModeIrregular) == 0 {
			continue
		}
		target, err := readlink(cur)
		if err != nil {
			return errors.New("network paths are not supported") // unreadable link: fail closed
		}
		if linkLeavesVolume(target, vol) {
			return errors.New("network paths are not supported")
		}
	}
	return nil
}
