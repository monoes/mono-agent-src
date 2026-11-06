package stop

import (
	"fmt"
	"runtime"
	"strings"
)

// The binding rests on the filesystem doing what the OS promises: Windows share modes that refuse delete and rename,
// kqueue events that report a change. Both are only dependable on a local filesystem of the kinds below; on a network
// share, a FAT or exFAT volume, ReFS (128-bit file ids, the binding records 64) or anything unknown the probe refuses.
// Linux has no rule: its binding watches names with inotify, which cannot see a change made by another client of a
// network, FUSE or WSL-mounted (/mnt/c) filesystem, and that is NOT detected there (stated in the entry text).
// What it cannot tell: a cloud-synced folder (OneDrive, iCloud Drive, Dropbox) on a local NTFS or APFS volume is
// indistinguishable here, a FUSE or WSL mount that reports NTFS or APFS, and a volume whose type changes later.

// volumeAllowed is the rule, portable so it is tested everywhere. fsType is the filesystem name the OS reports.
func volumeAllowed(goos, fsType string, remote bool) error {
	switch goos {
	case "windows":
		if remote {
			return fmt.Errorf("the volume is a network share: share modes are not dependable there, so the pinned binding is refused")
		}
		if !strings.EqualFold(fsType, "NTFS") {
			return fmt.Errorf("the volume is %q: only NTFS gives stable 64-bit file ids and enforced share modes, so the pinned binding is refused", fsType)
		}
	case "darwin":
		if remote {
			return fmt.Errorf("the volume is not local: kqueue reports only local changes, so the pinned binding is refused")
		}
		if t := strings.ToLower(fsType); t != "apfs" && t != "hfs" {
			return fmt.Errorf("the volume is %q: only APFS and HFS+ are trusted for kqueue vnode events, so the pinned binding is refused", fsType)
		}
	}
	return nil
}

// Test seams: the OS call and the OS the rule is applied for.
var (
	volumeClassFn = volumeClass
	volumeGOOS    = runtime.GOOS
)

// checkVolume asks the OS about the volume of dir and applies volumeAllowed; any error fails closed.
func checkVolume(dir string) error {
	fs, remote, err := volumeClassFn(dir)
	if err != nil {
		return fmt.Errorf("the filesystem of %s cannot be identified, so the pinned binding is refused: %w", dir, err)
	}
	return volumeAllowed(volumeGOOS, fs, remote)
}
