//go:build windows

package update

import "os"

// Windows has no owner check that means the same: a binary there is only ever reported, never replaced from here.
func ownedByUser(fi os.FileInfo) bool { return false }
