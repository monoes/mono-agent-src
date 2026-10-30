//go:build linux

package main

import (
	"errors"
	"testing"
)

func TestOnNetworkFilesystemUsesTheStatfsType(t *testing.T) {
	old := statfsType
	defer func() { statfsType = old }()
	for typ, want := range map[uint32]bool{0x6969: true, 0xFF534D42: true, 0x01021997: true, 0xEF53: false} {
		statfsType = func(string) (uint32, error) { return typ, nil }
		if got := onNetworkFilesystem("/x"); got != want {
			t.Errorf("%#x: %v", typ, got)
		}
	}
	statfsType = func(string) (uint32, error) { return 0, errors.New("boom") }
	if onNetworkFilesystem("/x") {
		t.Fatal("an unknown filesystem is not treated as network")
	}
	// the real call works on a real directory (a test temp dir is never NFS here)
	statfsType = old
	if _, err := statfsType(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}
