package sdconfine

import (
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
)

func specForTest() Spec {
	return Spec{Work: "/w/crit-1", Lim: finish.Limits{CPUSeconds: 7, AddressSpace: 5 << 30, OpenFiles: 33, FileSize: 4096, Procs: 3001}, PrivatePIDs: true}
}

// The golden list: the exact properties of a unit, in order. A property dropped, added or reworded fails this on purpose.
var goldenProps = []string{
	"PrivateNetwork=yes",
	"PrivateIPC=yes",
	"RestrictAddressFamilies=AF_UNIX",
	"ProtectSystem=strict",
	"ProtectHome=tmpfs",
	"TemporaryFileSystem=/run",
	"InaccessiblePaths=-/var/run -/mnt -/media",
	"PrivateTmp=yes",
	"PrivateDevices=yes",
	"ProtectKernelTunables=yes",
	"ProtectKernelModules=yes",
	"ProtectKernelLogs=yes",
	"ProtectControlGroups=yes",
	"ProtectClock=yes",
	"BindPaths=/w/crit-1",
	"PrivateUsers=yes",
	"PrivatePIDs=yes",
	"ProtectProc=invisible",
	"ProcSubset=pid",
	"NoNewPrivileges=yes",
	"CapabilityBoundingSet=",
	"RestrictNamespaces=yes",
	"LockPersonality=yes",
	"MemoryDenyWriteExecute=no",
	"SystemCallFilter=@system-service",
	"SystemCallFilter=~keyctl add_key request_key",
	"UMask=0077",
	"KeyringMode=private",
	"MemoryMax=2147483648",
	"MemorySwapMax=0",
	"TasksMax=512",
	"CPUQuota=200%",
	"LimitCPU=7",
	"LimitAS=5368709120",
	"LimitNOFILE=33",
	"LimitFSIZE=4096",
	"LimitCORE=0",
	"RuntimeMaxSec=22",
	"KillMode=control-group",
	"SendSIGKILL=yes",
	"TimeoutStopSec=5",
}

func TestThePropertyListIsTheDesignsExactlyAndNeverLimitsTheProcessCountPerUser(t *testing.T) {
	got := Properties(specForTest())
	if strings.Join(got, "\n") != strings.Join(goldenProps, "\n") {
		t.Fatalf("properties changed:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(goldenProps, "\n"))
	}
	for _, p := range got {
		if strings.HasPrefix(p, "LimitNPROC") {
			t.Fatal("RLIMIT_NPROC counts the whole user's processes; TasksMax bounds the unit")
		}
	}
}

// Revert proof for every confinement property: the builder's self-check refuses a list that lacks any one of them, and says
// which.
func TestTheSelfCheckRefusesAListMissingAnyOneProperty(t *testing.T) {
	s := specForTest()
	if err := CheckProperties(Properties(s), s); err != nil {
		t.Fatal(err)
	}
	for i, p := range goldenProps {
		short := append(append([]string(nil), goldenProps[:i]...), goldenProps[i+1:]...)
		err := CheckProperties(short, s)
		var u *Unavailable
		if !errors.As(err, &u) || u.Missing != MissingProperty || !strings.Contains(err.Error(), p) {
			t.Errorf("without %q: %v", p, err)
		}
		// and a property with its value changed is as bad as a missing one
		if k, v, ok := strings.Cut(p, "="); ok && !strings.HasPrefix(p, "SystemCallFilter") {
			mut := append([]string(nil), goldenProps...)
			mut[i] = k + "=" + v + "X"
			if CheckProperties(mut, s) == nil && v != "" {
				t.Errorf("%s with a different value passed the self-check", p)
			}
		}
	}
	// an extra property that widens nothing the list controls is not refused, but one that re-opens the network is
	if err := CheckProperties(append(Properties(s), "PrivateNetwork=no"), s); err == nil {
		t.Error("a later PrivateNetwork=no overrode the earlier one and was accepted")
	}
	if err := CheckProperties(append(Properties(s), "BindPaths=/home"), s); err == nil {
		t.Error("an extra BindPaths was accepted")
	}
}

func TestPrivatePIDsIsOptionalOnlyWhereTheManagerCannotDoIt(t *testing.T) {
	s := specForTest()
	s.PrivatePIDs = false
	for _, p := range Properties(s) {
		if strings.HasPrefix(p, "PrivatePIDs") {
			t.Fatal("PrivatePIDs asked on an old manager")
		}
	}
	if err := CheckProperties(Properties(s), s); err != nil {
		t.Fatalf("an old manager's list must pass its own check: %v", err)
	}
	s.PrivatePIDs = true
	if err := CheckProperties(Properties(Spec{Work: s.Work, Lim: s.Lim}), s); err == nil {
		t.Fatal("PrivatePIDs required of a new manager but not checked")
	}
}

func TestTheLimitsOfTheSandboxBecomeUnitPropertiesAndCapsCanOnlyLower(t *testing.T) {
	s := specForTest()
	s.Caps = Caps{MemoryBytes: 1 << 40, Tasks: 100000, CPUPercent: 10000}
	joined := strings.Join(Properties(s), "\n")
	for _, want := range []string{"MemoryMax=2147483648", "TasksMax=512", "CPUQuota=200%"} {
		if !strings.Contains(joined, want) {
			t.Errorf("a cap above the design's was honoured: missing %s", want)
		}
	}
	s.Caps = Caps{MemoryBytes: 256 << 20, Tasks: 64, CPUPercent: 50}
	joined = strings.Join(Properties(s), "\n")
	for _, want := range []string{"MemoryMax=268435456", "TasksMax=64", "CPUQuota=50%"} {
		if !strings.Contains(joined, want) {
			t.Errorf("a lower cap was not applied: missing %s", want)
		}
	}
	s.Caps = Caps{MemoryBytes: -5, Tasks: -1, CPUPercent: -9}
	if joined := strings.Join(Properties(s), "\n"); !strings.Contains(joined, "MemoryMax=2147483648") || !strings.Contains(joined, "TasksMax=512") {
		t.Errorf("a negative cap was honoured: %s", joined)
	}
}

func TestTheHiddenRootAndTheWorkPathAreEscapedForTheManagerOrRefused(t *testing.T) {
	s := specForTest()
	s.Hide = "/srv/project"
	if !strings.Contains(strings.Join(Properties(s), "\n"), "InaccessiblePaths=-/var/run -/mnt -/media /srv/project") {
		t.Fatalf("the live root is not hidden: %v", Properties(s))
	}
	if err := CheckProperties(Properties(s), s); err != nil {
		t.Fatal(err)
	}
	s.Hide = ""
	for _, bad := range []string{"", "rel/path", "/a b", "/a:b", "/a%b", "/a$b", "/a\nb", "/a\tb", "/a'b", "/a\\b", "/a\"b", "/a/../b", "/a//b", "/a/", "/x\x00y"} {
		if SafePath(bad) == nil {
			t.Errorf("%q was accepted as a path for a unit property", bad)
		}
	}
	for _, ok := range []string{"/home/me/.monoagent/scratch/check-1/work", "/var/lib/x_y-z.1"} {
		if err := SafePath(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
}
