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
		"empty body":          frame(""),
		"unknown field":       frame(`{"v":1,"token_id":"t","profile":"p","scope":"s","nv":"a","ns":"b","extra":1}`),
		"trailing data":       frame(`{"v":1} {"v":2}`),
		"not json":            frame(`not json`),
		"too long":            bytes.NewReader(binary.BigEndian.AppendUint32(nil, maxFrame+1)),
		"huge length":         bytes.NewReader(binary.BigEndian.AppendUint32(nil, 1<<31)),
		"short body":          bytes.NewReader(append(binary.BigEndian.AppendUint32(nil, 50), "{}"...)),
		"short header":        bytes.NewReader([]byte{0, 0}),
		"wrong type":          frame(`{"v":"one"}`),
		"array not obj":       frame(`[1,2]`),
		"null body":           frame(`null`),
		"nested garbage":      frame(`{"v":1,"token_id":{"a":1}}`),
		"stray brace":         frame(`{"v":1,"token_id":"t","profile":"p","scope":"s","nv":"a","ns":"b"}}`),
		"stray bracket":       frame(`{"v":1,"token_id":"t","profile":"p","scope":"s","nv":"a","ns":"b"}]`),
		"duplicate key":       frame(`{"v":1,"token_id":"a","token_id":"t","profile":"p","scope":"s","nv":"a","ns":"b"}`),
		"key in another case": frame(`{"v":1,"TOKEN_ID":"t","profile":"p","scope":"s","nv":"a","ns":"b"}`),
		"other order":         frame(`{"token_id":"t","v":1,"profile":"p","scope":"s","nv":"a","ns":"b"}`),
		"with spaces":         frame(`{"v": 1, "token_id":"t","profile":"p","scope":"s","nv":"a","ns":"b"}`),
		"empty object":        frame(`{}`),
	} {
		var w wireRequest
		err := readFrame(r, &w)
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := writeFrame(&b, wireRequest{TokenID: strings.Repeat("x", maxFrame)}); err == nil {
		t.Error("an oversized frame was written")
	}
}
