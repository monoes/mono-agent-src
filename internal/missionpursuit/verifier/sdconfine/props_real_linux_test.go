//go:build linux

package sdconfine

import "strings"

// propRow is one property: what to set, which property to read back from the running unit, and what the read-back must show when
// the property is applied. pinned: the manager's default equals the value, so absence cannot be seen by reading it back (the
// property is explicit so a changed default or a drop-in cannot weaken the unit; its revert proof is the self-check refusal).
type propRow struct {
	set, show string
	ok        func(out string) bool
	pinned    bool
}

func reads(want string) func(string) bool {
	return func(out string) bool { return strings.Contains(out, want) }
}

func rows() []propRow {
	eq := func(set string) propRow {
		return propRow{set: set, show: strings.SplitN(set, "=", 2)[0], ok: reads(set)}
	}
	return []propRow{
		eq("PrivateNetwork=yes"), eq("PrivateIPC=yes"), eq("RestrictAddressFamilies=AF_UNIX"), eq("ProtectSystem=strict"), eq("ProtectHome=tmpfs"),
		{set: "TemporaryFileSystem=/run", show: "TemporaryFileSystem", ok: func(o string) bool { return strings.Contains(o, "/run") && !strings.Contains(o, "/run/") }},
		{set: "TemporaryFileSystem=/dev/shm:mode=1777", show: "TemporaryFileSystem", ok: reads("/dev/shm:mode=1777")},
		{set: "TemporaryFileSystem=/dev/pts", show: "TemporaryFileSystem", ok: reads("/dev/pts")},
		eq("OOMPolicy=kill"),
		{set: "InaccessiblePaths=-/var/run -/mnt -/media", show: "InaccessiblePaths", ok: reads("-/mnt")},
		eq("PrivateTmp=yes"), eq("PrivateDevices=yes"), eq("ProtectKernelTunables=yes"), eq("ProtectKernelModules=yes"), eq("ProtectKernelLogs=yes"),
		eq("ProtectControlGroups=yes"), eq("ProtectClock=yes"),
		{set: "BindPaths=<work>", show: "BindPaths", ok: reads("/crit-")},
		eq("PrivateUsers=yes"), eq("PrivatePIDs=yes"), eq("ProtectProc=invisible"), eq("ProcSubset=pid"), eq("NoNewPrivileges=yes"),
		{set: "CapabilityBoundingSet=", show: "CapabilityBoundingSet", ok: func(o string) bool { return strings.TrimSpace(o) == "CapabilityBoundingSet=" }},
		eq("RestrictNamespaces=yes"), eq("LockPersonality=yes"),
		{set: "MemoryDenyWriteExecute=no", show: "MemoryDenyWriteExecute", ok: reads("=no"), pinned: true},
		// the read-back shows the allowed list: the filter is on when it is a list of calls, and keyctl's are out of it
		{set: "SystemCallFilter=@system-service", show: "SystemCallFilter", ok: func(o string) bool { return strings.Contains(o, " execve ") && strings.Contains(o, " write") }},
		{set: "SystemCallFilter=~keyctl add_key request_key", show: "SystemCallFilter", ok: func(o string) bool { return !strings.Contains(o, "request_key") && strings.Contains(o, " execve ") }},
		eq("UMask=0077"), eq("KeyringMode=private"), eq("MemoryMax=268435456"), eq("MemorySwapMax=0"), eq("TasksMax=64"),
		{set: "CPUQuota=100%", show: "CPUQuotaPerSecUSec", ok: reads("CPUQuotaPerSecUSec=1s")},
		eq("LimitCPU=300"), eq("LimitAS=8589934592"), eq("LimitNOFILE=777"), eq("LimitFSIZE=4096"), eq("LimitCORE=0"),
		{set: "RuntimeMaxSec=315", show: "RuntimeMaxUSec", ok: reads("RuntimeMaxUSec=5min 15s")},
		{set: "KillMode=control-group", show: "KillMode", ok: reads("=control-group"), pinned: true},
		{set: "SendSIGKILL=yes", show: "SendSIGKILL", ok: reads("=yes"), pinned: true},
		{set: "TimeoutStopSec=5", show: "TimeoutStopUSec", ok: reads("TimeoutStopUSec=5s")},
	}
}
