//go:build !darwin && !windows

package stop

// volumeClass: no volume rule applies off macOS and Windows (Linux watches names with inotify).
func volumeClass(string) (string, bool, error) { return "", false, nil }
