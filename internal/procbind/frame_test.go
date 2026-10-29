package procbind

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestFrameRoundTripAndLimits(t *testing.T) {
	var b bytes.Buffer
	in := wireRequest{V: 1, TokenID: "t", Profile: "p", Scope: "s", NV: "aa", NS: "bb"}
	if err := writeFrame(&b, in); err != nil {
		t.Fatal(err)
	}
	var out wireRequest
	if err := readFrame(&b, &out); err != nil || out != in {
		t.Fatalf("round trip: %+v %v", out, err)
	}
	frame := func(body string) *bytes.Reader {
		return bytes.NewReader(append(binary.BigEndian.AppendUint32(nil, uint32(len(body))), body...))
	}
	for name, r := range map[string]*bytes.Reader{
		"empty body":     frame(""),
		"unknown field":  frame(`{"v":1,"token_id":"t","profile":"p","scope":"s","nv":"a","ns":"b","extra":1}`),
		"trailing data":  frame(`{"v":1} {"v":2}`),
		"not json":       frame(`not json`),
		"too long":       bytes.NewReader(binary.BigEndian.AppendUint32(nil, maxFrame+1)),
		"huge length":    bytes.NewReader(binary.BigEndian.AppendUint32(nil, 1<<31)),
		"short body":     bytes.NewReader(append(binary.BigEndian.AppendUint32(nil, 50), "{}"...)),
		"short header":   bytes.NewReader([]byte{0, 0}),
		"wrong type":     frame(`{"v":"one"}`),
		"array not obj":  frame(`[1,2]`),
		"null body":      frame(`null`),
		"nested garbage": frame(`{"v":1,"token_id":{"a":1}}`),
	} {
		var w wireRequest
		err := readFrame(r, &w)
		if name == "null body" {
			continue // null decodes to the zero value; the server refuses it on content (version 0)
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := writeFrame(&b, wireRequest{TokenID: strings.Repeat("x", maxFrame)}); err == nil {
		t.Error("an oversized frame was written")
	}
}
