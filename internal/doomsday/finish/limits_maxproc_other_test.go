//go:build !darwin && !windows

package finish

func kernMaxProcPerUID() (uint64, bool) { return 0, false }
