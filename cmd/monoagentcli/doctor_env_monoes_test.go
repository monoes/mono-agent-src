package main

import (
	"testing"

	"github.com/monoes/mono-agent/internal/account/accounttest"
	"github.com/monoes/mono-agent/internal/health"
)

// doctor reports the machine's monoes.me session in one core row, and it
// stays usable (exit 0, the row is not required) in every state.
func TestDoctorReportsTheMonoesAccount(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   accounttest.Mode
		status health.Status
		fix    string
	}{
		{"signed in", accounttest.SignedIn, health.StatusOK, ""},
		{"locked", accounttest.LockedNoLogin, health.StatusFail, health.FixMonoesLogin},
		{"refused", accounttest.LockedRefused, health.StatusFail, health.FixMonoesLogin},
		{"dormant", accounttest.Dormant, health.StatusInfo, ""},
	} {
		accounttest.Install(t, tc.mode)
		out, err := runDoctor(t, t.TempDir(), "doctor", "--json", "--check", health.CheckMonoesAccount)
		if err != nil {
			t.Fatalf("%s: doctor: %v\n%s", tc.name, err, out)
		}
		row := resultByID(decodeDoctor(t, out), health.CheckMonoesAccount)
		fix := ""
		if row.Fix != nil {
			fix = row.Fix.ID
		}
		if row.Status != tc.status || fix != tc.fix || row.Group != health.GroupCore || row.Required {
			t.Errorf("%s: row %+v", tc.name, row)
		}
	}
}
