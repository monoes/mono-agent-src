package coderhost

import "testing"

func TestPrivateACLDecisions(t *testing.T) {
	const me, other = "S-1-5-21-1-2-3-1001", "S-1-5-21-1-2-3-1002"
	const (
		full = uint32(0x1F01FF)
		read = uint32(0x120089)
	)
	allow := func(sid string, m uint32) aclEntry { return aclEntry{aceAllow, sid, m} }
	ok := []aclEntry{allow(me, full), allow(sidSystem, full), allow(sidAdministrator, full)}
	cases := []struct {
		name     string
		owner    string
		hasDACL  bool
		aces     []aclEntry
		ownerToo string
		want     bool
	}{
		{"default profile directory", me, true, ok, "", true},
		{"others may read", me, true, append(append([]aclEntry{}, ok...), allow("S-1-1-0", read)), "", true},
		{"everyone may write", me, true, append(append([]aclEntry{}, ok...), allow("S-1-1-0", full)), "", false},
		{"users may add files", me, true, append(append([]aclEntry{}, ok...), allow("S-1-5-32-545", 0x2)), "", false},
		{"authenticated users may modify", me, true, append(append([]aclEntry{}, ok...), allow("S-1-5-11", 0x1301BF)), "", false},
		{"another user may delete", me, true, append(append([]aclEntry{}, ok...), allow(other, 0x10000)), "", false},
		{"another user may take ownership", me, true, append(append([]aclEntry{}, ok...), allow(other, 0x80000)), "", false},
		{"generic write", me, true, append(append([]aclEntry{}, ok...), allow(other, 0x40000000)), "", false},
		{"a deny ACE grants nothing", me, true, append(append([]aclEntry{}, ok...), aclEntry{aceDeny, "S-1-1-0", full}), "", true},
		{"an audit ACE grants nothing", me, true, append(append([]aclEntry{}, ok...), aclEntry{aceIgnored, "S-1-1-0", full}), "", true},
		{"an ACE type not understood", me, true, append(append([]aclEntry{}, ok...), aclEntry{aceUnknown, me, 0}), "", false},
		{"owned by someone else", other, true, ok, "", false},
		{"owned by Administrators, not our token's owner", sidAdministrator, true, ok, "", false},
		{"owned by the token's default owner (elevated)", sidAdministrator, true, ok, sidAdministrator, true},
		{"no DACL means everyone has full access", me, false, nil, "", false},
		{"empty DACL is private (nobody)", me, true, nil, "", true},
		{"unknown owner", "", true, ok, "", false},
		{"sids compare case-insensitively", "s-1-5-21-1-2-3-1001", true, ok, "", true},
	}
	for _, c := range cases {
		if got := privateACL(c.owner, c.hasDACL, c.aces, me, c.ownerToo); got != c.want {
			t.Errorf("%s: privateACL = %v, want %v", c.name, got, c.want)
		}
	}
	if privateACL(me, true, ok, "", "") {
		t.Error("an unknown current user was accepted")
	}
}

// The descriptor a registry directory is created with is protected and names the user and SYSTEM only.
func TestRegistryDirSDDLGolden(t *testing.T) {
	const want = "D:PAI(A;OICI;FA;;;S-1-5-21-1-2-3-1001)(A;OICI;FA;;;SY)"
	if got := registryDirSDDL("S-1-5-21-1-2-3-1001"); got != want {
		t.Fatalf("SDDL = %q, want %q", got, want)
	}
}
