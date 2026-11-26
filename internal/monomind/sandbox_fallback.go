package monomind

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

// What a turn does when its runtime lacks the sandbox mode it asks for
// (monomind#482, capability CapAgentExecSandboxFallback). The user picks
// one in Settings; Run is the default and is exactly what mono-agent did
// before the setting existed.
const (
	// SandboxFallbackFlag is the agent exec flag that carries the choice.
	SandboxFallbackFlag = "--sandbox-fallback"
	// SandboxFallbackRun passes no flag: the turn runs without a sandbox.
	SandboxFallbackRun = "run"
	// SandboxFallbackStrictest runs the strictest sandbox the runtime has.
	SandboxFallbackStrictest = "strictest"
	// SandboxFallbackFail refuses the turn (monomind errors "unsupported").
	SandboxFallbackFail = "fail"
	// SandboxFallbackEnv overrides the saved setting.
	SandboxFallbackEnv = "MONOAGENT_SANDBOX_FALLBACK"
)

// SandboxFallbackValues lists the accepted values, default first.
var SandboxFallbackValues = []string{SandboxFallbackRun, SandboxFallbackStrictest, SandboxFallbackFail}

// ParseSandboxFallback normalises v; ok is false for anything but a value
// of SandboxFallbackValues (case and surrounding space are forgiven).
func ParseSandboxFallback(v string) (string, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	return v, slices.Contains(SandboxFallbackValues, v)
}

// SandboxFallbackProvider returns the saved setting. cmd installs it at
// startup (this package must not reach the database); it is read once per
// turn, so a long-running process sees a change without a restart. nil, or
// a value that isn't one of SandboxFallbackValues, means SandboxFallbackRun.
var SandboxFallbackProvider func() string

// SandboxFallbackSetting is the effective setting: the environment
// override, else the saved value, else SandboxFallbackRun.
func SandboxFallbackSetting() string {
	if v, ok := ParseSandboxFallback(os.Getenv(SandboxFallbackEnv)); ok {
		return v
	}
	if SandboxFallbackProvider != nil {
		if v, ok := ParseSandboxFallback(SandboxFallbackProvider()); ok {
			return v
		}
	}
	return SandboxFallbackRun
}

// SandboxFallbackIgnoredNote is shown when the setting asks for strictest
// or fail but monomind can't honour it.
const SandboxFallbackIgnoredNote = "the sandbox-fallback setting was ignored: this monomind lacks " + CapAgentExecSandboxFallback + "; ran as with 'run'"

// SandboxFallbackRefusedNote marks a turn the 'fail' setting refuses on its
// own, because this monomind cannot refuse it (no CapAgentExecSandboxFallback).
const SandboxFallbackRefusedNote = "the sandbox-fallback setting is 'fail' and this runtime has no sandbox for the turn"

// SandboxFallbackPlan is SandboxArgs plus the user's fallback setting. With
// fallback "run", a runtime that has the mode, claude, or an empty mode it
// returns SandboxArgs' answer unchanged. Otherwise (strictest or fail,
// monomind has the flag, the runtime's scan lists modes but not mode) it
// asks monomind for the mode together with the fallback and returns active.
// The status is then the pre-start guess (restricted for strictest,
// unsupported for fail: monomind refuses); the start event's
// sandbox_applied settles it (AppliedSandboxStatus). note says why the
// setting did not apply, when monomind lacks the capability.
func SandboxFallbackPlan(caps *CapabilitySet, modes []string, runtime, mode, fallback string) (args []string, effective string, active bool, note string) {
	args, effective = SandboxArgs(caps, modes, runtime, mode)
	if fallback != SandboxFallbackStrictest && fallback != SandboxFallbackFail {
		return args, effective, false, ""
	}
	if mode == "" || mode == SandboxFull || runtime == "claude" || !caps.Has(CapAgentExecSandbox) || modes == nil || slices.Contains(modes, mode) {
		return args, effective, false, ""
	}
	if !caps.Has(CapAgentExecSandboxFallback) {
		if fallback == SandboxFallbackFail {
			return args, SandboxStatusUnsupported, false, SandboxFallbackRefusedNote
		}
		return args, effective, false, SandboxFallbackIgnoredNote
	}
	args = []string{SandboxFlag, mode, SandboxFallbackFlag, fallback}
	if fallback == SandboxFallbackFail {
		return args, SandboxStatusUnsupported, true, ""
	}
	return args, SandboxStatusRestricted, true, ""
}

// SandboxMissing reports a start event's verdict under which the turn runs
// with no sandbox at all. "scoped" (claude's scoped access) and "restricted"
// (monomind's stricter fallback) limit the turn, so they are not missing, and
// "" is a turn that asked for none.
func SandboxMissing(status string) bool {
	switch status {
	case SandboxStatusUnsupported, SandboxStatusAwaitingMonomind, SandboxStatusNeedsMonomind, SandboxStatusOff:
		return true
	}
	return false
}

// UnsandboxedWarning is what such a turn says about itself.
func UnsandboxedWarning(runtime, status string) string {
	return fmt.Sprintf("this turn is running without a sandbox (%s: %s): the agent runtime is not confined to a working folder", runtime, status)
}

// AppliedSandboxStatus is the verdict once monomind reports sandbox_applied
// for a turn that used the fallback: sandboxed when it is the mode asked
// for, off when it is "full" (no sandbox at all), else restricted.
func AppliedSandboxStatus(requested, applied string) string {
	switch applied {
	case requested:
		return SandboxStatusSandboxed
	case SandboxFull:
		return SandboxStatusOff
	}
	return SandboxStatusRestricted
}
