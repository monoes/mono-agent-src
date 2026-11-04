// Package testmode says whether this process is a go-test binary. testhome.Main
// sets On; production code never does, and nothing in the environment can: a
// variable an agent could set would let it switch a security check off.
// (Production packages read this instead of importing "testing".)
package testmode

// On is true in a test binary that went through testhome.Main.
var On bool
