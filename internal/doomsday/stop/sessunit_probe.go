package stop

import "os/exec"

// STUB (red commit): replaced by the real logic in the next commit.
func fmtSessStamp(sec int64, usec int64) string                               { return "" }
func checkSessCmd(cmd *exec.Cmd) error                                        { return nil }
func sessProbeShape(self int, leader, plain, grp, own SessProc) error         { return nil }
func sessProbeFinds(scan []SessProc, leader SessProc, want ...SessProc) error { return nil }
