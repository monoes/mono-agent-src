package stop

// SetHostGOOSForTest makes the package act as the host OS goos for the rest of the process and returns the restore. It is
// for the test binaries of callers whose fakes (a cgroup kernel, a service manager) are of one platform: on another host
// the real containment of that host would otherwise be chosen. Production never calls it.
func SetHostGOOSForTest(goos string) (restore func()) {
	old := hostGOOS
	hostGOOS = goos
	return func() { hostGOOS = old }
}
