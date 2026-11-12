//go:build !linux && !darwin

package finish

import "os"

// statExtra: no change time or inode is available here; the host's periodic full digest covers the gap.
func statExtra(os.FileInfo) string { return "" }
