package operator

import (
	"strconv"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/workflow"
)

// What Marker names when a parent process, not this one, is marked: each
// starts with ancestorPrefix ("a parent process's CLAUDECODE" for any other
// agent marker).
const (
	ancestorPrefix      = "a parent process"
	ancestorWorkflowRun = "a parent process's " + workflow.EnvWorkflowRun
	ancestorAICaller    = "a parent process's " + browserctl.EnvAICaller
	ancestorAgentExec   = "a parent process (monomind agent exec)"
)

// ancestorMaxDepth is how many parent processes Marker looks at; a chain
// that goes on past them is ancestorTooDeep.
const ancestorMaxDepth = 32

// ancestorTooDeep is what Marker names for a parent chain deeper than
// ancestorMaxDepth.
var ancestorTooDeep = "a parent chain deeper than " + strconv.Itoa(ancestorMaxDepth)

// ancestorsInTests turns the parent check on inside a test binary. It is off
// there by default: `go test` run from an agent's session would make every
// test that stands for the operator see that session. ancestorsStopAt, when
// set, is a parent the check does not look at or above (a test's own
// process, so what runs the test does not count).
var (
	ancestorsInTests = false
	ancestorsStopAt  = 0
)
