package sdconfine

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
)

// Defaults of the caps (the design's); a Config can only lower them.
const (
	DefaultMemoryBytes = 2 << 30
	DefaultTasks       = 512
	DefaultCPUPercent  = 200
	// runtimeSlack is added to the criterion's wall clock for the manager's own backstop (RuntimeMaxSec) against a crashed host.
	runtimeSlack = 15
)

// Caps are the unit's resource caps.
type Caps struct {
	MemoryBytes int64
	Tasks       int
	CPUPercent  int
}

// normalized takes the design's value for a cap that is unset, not positive, or above the design's.
func (c Caps) normalized() Caps {
	if c.MemoryBytes <= 0 || c.MemoryBytes > DefaultMemoryBytes {
		c.MemoryBytes = DefaultMemoryBytes
	}
	if c.Tasks <= 0 || c.Tasks > DefaultTasks {
		c.Tasks = DefaultTasks
	}
	if c.CPUPercent <= 0 || c.CPUPercent > DefaultCPUPercent {
		c.CPUPercent = DefaultCPUPercent
	}
	return c
}

// Spec is what one unit's property list depends on.
type Spec struct {
	// Work is the check's working area: the ONLY path bound into the unit (the sandbox's copy of the subject and its scratch
	// folder are inside it).
	Work string
	// Hide is the live project folder, made inaccessible where it lies outside what the filesystem rules already hide.
	Hide string
	// Lim are the sandbox's limits, applied here as unit properties (the limits helper is not run inside the unit).
	Lim finish.Limits
	// Caps are the resource caps; PrivatePIDs is whether the manager supports it (systemd 256 and later).
	Caps        Caps
	PrivatePIDs bool
}

func (s Spec) inaccessible() string {
	v := "-/var/run -/mnt -/media"
	if s.Hide != "" {
		v += " -" + s.Hide // "-": a root that is not there (hidden under /home already) is not an error
	}
	return v
}

// Properties is the unit's property list, in the order they are passed. No property the manager does not know is dropped
// silently: the probe (New) runs this exact list, and a refusal names the property.
//
// Not limited: RLIMIT_NPROC (it counts every process of the user; TasksMax bounds the unit instead).
func Properties(s Spec) []string {
	c := s.Caps.normalized()
	p := []string{
		// network and IPC
		"PrivateNetwork=yes", "PrivateIPC=yes", "RestrictAddressFamilies=AF_UNIX",
		// filesystem
		"ProtectSystem=strict", "ProtectHome=tmpfs", "TemporaryFileSystem=/run", "InaccessiblePaths=" + s.inaccessible(),
		"PrivateTmp=yes", "PrivateDevices=yes",
		"ProtectKernelTunables=yes", "ProtectKernelModules=yes", "ProtectKernelLogs=yes", "ProtectControlGroups=yes", "ProtectClock=yes",
		"BindPaths=" + s.Work,
		// processes and users
		"PrivateUsers=yes",
	}
	if s.PrivatePIDs {
		p = append(p, "PrivatePIDs=yes")
	}
	p = append(p,
		"ProtectProc=invisible", "ProcSubset=pid", "NoNewPrivileges=yes", "CapabilityBoundingSet=", "RestrictNamespaces=yes",
		"LockPersonality=yes", "MemoryDenyWriteExecute=no",
		"SystemCallFilter=@system-service", "SystemCallFilter=~keyctl add_key request_key",
		"UMask=0077", "KeyringMode=private",
		// resources
		fmt.Sprintf("MemoryMax=%d", c.MemoryBytes), "MemorySwapMax=0", fmt.Sprintf("TasksMax=%d", c.Tasks), fmt.Sprintf("CPUQuota=%d%%", c.CPUPercent),
		fmt.Sprintf("LimitCPU=%d", s.Lim.CPUSeconds), fmt.Sprintf("LimitAS=%d", s.Lim.AddressSpace),
		fmt.Sprintf("LimitNOFILE=%d", s.Lim.OpenFiles), fmt.Sprintf("LimitFSIZE=%d", s.Lim.FileSize), "LimitCORE=0",
		fmt.Sprintf("RuntimeMaxSec=%d", s.Lim.CPUSeconds+runtimeSlack),
		"KillMode=control-group", "SendSIGKILL=yes", "TimeoutStopSec=5",
	)
	return p
}

// required is the builder's self-check list, kept separately from Properties on purpose: a property dropped from one and not the
// other is found, and nothing runs with a weaker fence.
func required(s Spec) []string {
	c := s.Caps.normalized()
	r := []string{
		"PrivateNetwork=yes", "PrivateIPC=yes", "RestrictAddressFamilies=AF_UNIX",
		"ProtectSystem=strict", "ProtectHome=tmpfs", "TemporaryFileSystem=/run",
		"PrivateTmp=yes", "PrivateDevices=yes",
		"ProtectKernelTunables=yes", "ProtectKernelModules=yes", "ProtectKernelLogs=yes", "ProtectControlGroups=yes", "ProtectClock=yes",
		"PrivateUsers=yes", "ProtectProc=invisible", "ProcSubset=pid", "NoNewPrivileges=yes", "CapabilityBoundingSet=",
		"RestrictNamespaces=yes", "LockPersonality=yes", "MemoryDenyWriteExecute=no",
		"SystemCallFilter=@system-service", "SystemCallFilter=~keyctl add_key request_key",
		"UMask=0077", "KeyringMode=private", "MemorySwapMax=0", "LimitCORE=0",
		"KillMode=control-group", "SendSIGKILL=yes", "TimeoutStopSec=5",
		"InaccessiblePaths=" + s.inaccessible(), "BindPaths=" + s.Work,
		"MemoryMax=" + fmt.Sprint(c.MemoryBytes), "TasksMax=" + fmt.Sprint(c.Tasks), "CPUQuota=" + fmt.Sprint(c.CPUPercent) + "%",
		"LimitCPU=" + fmt.Sprint(s.Lim.CPUSeconds), "LimitAS=" + fmt.Sprint(s.Lim.AddressSpace),
		"LimitNOFILE=" + fmt.Sprint(s.Lim.OpenFiles), "LimitFSIZE=" + fmt.Sprint(s.Lim.FileSize),
		"RuntimeMaxSec=" + fmt.Sprint(s.Lim.CPUSeconds+runtimeSlack),
	}
	if s.PrivatePIDs {
		r = append(r, "PrivatePIDs=yes")
	}
	return r
}

// CheckProperties refuses a list that is not exactly the required one for s: a property missing or changed (named), or one
// that is not wanted (a later PrivateNetwork=no, an extra BindPaths).
func CheckProperties(props []string, s Spec) error {
	want := map[string]int{}
	for _, r := range required(s) {
		want[r]++
	}
	for _, p := range props {
		if want[p] == 0 {
			return unavailable(MissingProperty, "the unit carries %q, which the design does not allow", p)
		}
		want[p]--
	}
	var missing []string
	for r, n := range want {
		if n > 0 {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return unavailable(MissingProperty, "the unit lacks %s", strings.Join(missing, ", "))
	}
	return nil
}

// SafePath says whether p can be given to the manager as a path in a property or an environment value without escaping: an
// absolute, clean path that is not the root, with no space, colon, percent, dollar, quote, backslash, backtick or control
// character.
func SafePath(p string) error {
	if p == "" || !filepath.IsAbs(p) || p == "/" || filepath.Clean(p) != p {
		return fmt.Errorf("%q is not an absolute, clean path", p)
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" :%$'\"\\`", r) {
			return fmt.Errorf("%q has a character a unit property cannot carry", p)
		}
	}
	return nil
}
