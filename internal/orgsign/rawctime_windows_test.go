//go:build windows

package orgsign

// rawDirCtime: Windows has no directory ctime here (the guard treats it as untrusted as a whole).
func rawDirCtime(string) int64 { return 0 }
