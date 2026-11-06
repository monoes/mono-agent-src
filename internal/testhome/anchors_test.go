package testhome

import (
	"reflect"
	"testing"
)

const psSample = `    1 /sbin/launchd
  101 /usr/bin/zsh -l
  202 monoagent-run-2-793cNb9af24819ed4234a0045.service 2147483647
  203 monoagent-probe-ab12cd 2147483647
  204 /bin/sleep 2147483647
  205 /Users/u/bin/monoagent-run-helper
bad line
`

func TestParseAnchorsFindsOnlyTheAnchorsByArgvZero(t *testing.T) {
	got := parseAnchors(psSample)
	if len(got) != 2 || got[202] == "" || got[203] == "" {
		t.Fatalf("anchors: %v", got)
	}
}

func TestNewAnchorsAreTheOnesThatAppearedOrChanged(t *testing.T) {
	before := parseAnchors(psSample)
	after := parseAnchors(psSample + "  300 monoagent-run-3-ff.service 2147483647\n")
	if got := newAnchors(before, after); !reflect.DeepEqual(got, []int{300}) {
		t.Fatalf("new: %v", got)
	}
	if got := newAnchors(before, before); len(got) != 0 {
		t.Fatalf("an anchor that was there before counted: %v", got)
	}
	// a pid reused by another anchor is new
	reused := map[int]string{202: "monoagent-run-9-aa.service 2147483647"}
	if got := newAnchors(before, reused); !reflect.DeepEqual(got, []int{202}) {
		t.Fatalf("reused pid: %v", got)
	}
}

func TestCheckAnchorsIsQuietOffMacOSAndWithNothingNew(t *testing.T) {
	if got := checkAnchors(psAnchors()); got != "" {
		t.Fatalf("%s", got)
	}
}
