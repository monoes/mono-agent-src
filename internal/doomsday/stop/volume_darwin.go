//go:build darwin

package stop

import "golang.org/x/sys/unix"

// NOT VERIFIED on a real macOS host (compiles with GOOS=darwin go vet).
func volumeClass(dir string) (string, bool, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return "", false, err
	}
	name := make([]byte, 0, len(st.Fstypename))
	for _, c := range st.Fstypename {
		if c == 0 {
			break
		}
		name = append(name, byte(c))
	}
	return string(name), st.Flags&unix.MNT_LOCAL == 0, nil
}
