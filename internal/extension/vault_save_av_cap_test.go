package extension

import (
	"encoding/base64"
	"testing"
)

// Audio and video are held to the same cap as everything else: exactly the cap
// passes, one byte more is refused before the sink, and so is a payload whose
// base64 is too long to be within the cap whatever it decodes to.
func TestVaultSave_AudioAndVideoCap(t *testing.T) {
	for mime, head := range map[string][]byte{"audio/mpeg": avSamples()["audio/mpeg"], "video/mp4": avSamples()["video/mp4"]} {
		sized := func(n int) string {
			b := make([]byte, n)
			copy(b, head)
			return base64.StdEncoding.EncodeToString(b)
		}
		sink := &fakeVaultSink{}
		if _, err := callVaultSave(t, sink, vaultParams(map[string]any{"mime": mime, "data": sized(VaultSaveMaxBytes)})); err != nil {
			t.Errorf("%s of exactly the cap: %v", mime, err)
		}
		refusedInvalid(t, mime+" one byte over the cap", sink, vaultParams(map[string]any{"mime": mime, "data": sized(VaultSaveMaxBytes + 1)}))
		refusedInvalid(t, mime+" with an encoded length far over the cap", sink, vaultParams(map[string]any{"mime": mime, "data": sized(VaultSaveMaxBytes + 4<<20)}))
	}
}

// An M4A and an MP4 are one container: either label takes either file, and the
// label only picks the stored extension (SECURITY.md says so).
func TestVaultSave_Mp4AndM4aShareAContainer(t *testing.T) {
	samples := avSamples()
	for _, c := range []struct{ mime, bytesOf, ext string }{
		{"audio/mp4", "video/mp4", ".m4a"},
		{"video/mp4", "audio/mp4", ".mp4"},
	} {
		sink := &fakeVaultSink{}
		if _, err := callVaultSave(t, sink, vaultParams(map[string]any{
			"mime": c.mime, "data": base64.StdEncoding.EncodeToString(samples[c.bytesOf]),
		})); err != nil {
			t.Errorf("%s label on %s bytes: %v", c.mime, c.bytesOf, err)
			continue
		}
		if sink.got[0].Ext != c.ext {
			t.Errorf("%s label stored as %s, want %s", c.mime, sink.got[0].Ext, c.ext)
		}
	}
}
