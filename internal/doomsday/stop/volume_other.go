//go:build !darwin && !windows

package stop

// volumeClass: no volume rule applies off macOS and Windows. On Linux (inotify) a database on a network, FUSE or WSL-mounted
// filesystem is therefore NOT detected: another client's changes raise no event here (see the Linux entry text).
func volumeClass(string) (string, bool, error) { return "", false, nil }
