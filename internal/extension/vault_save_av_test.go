package extension

import (
	"bytes"
	"encoding/base64"
	"testing"
)

// avSamples are the smallest bytes http.DetectContentType calls each audio and
// video type, padded.
func avSamples() map[string][]byte {
	pad := func(head string) []byte {
		b := make([]byte, 64)
		copy(b, head)
		return b
	}
	return map[string][]byte{
		"audio/mpeg": pad("ID3\x03\x00\x00\x00\x00\x00\x00"),
		"audio/wav":  pad("RIFF\x24\x00\x00\x00WAVEfmt "),
		"audio/ogg":  pad("OggS\x00\x02"),
		"audio/mp4":  pad("\x00\x00\x00\x18ftypM4A \x00\x00\x00\x00mp42isom"),
		"video/mp4":  pad("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"),
		"video/webm": pad("\x1a\x45\xdf\xa3"),
	}
}

// Audio and video are saved like any other type: the sink gets the file under
// the extension of its type, and the bytes must be what the type says.
func TestVaultSave_AudioAndVideo(t *testing.T) {
	wantExt := map[string]string{
		"audio/mpeg": ".mp3", "audio/wav": ".wav", "audio/ogg": ".ogg",
		"audio/mp4": ".m4a", "video/mp4": ".mp4", "video/webm": ".webm",
	}
	for mime, data := range avSamples() {
		sink := &fakeVaultSink{}
		if _, err := callVaultSave(t, sink, vaultParams(map[string]any{
			"mime": mime, "name": "clip" + wantExt[mime], "data": base64.StdEncoding.EncodeToString(data),
		})); err != nil {
			t.Errorf("%s: %v", mime, err)
			continue
		}
		f := sink.got[0]
		if f.MIME != mime || f.Ext != wantExt[mime] || !bytes.Equal(f.Data, data) {
			t.Errorf("%s: sink got %+v", mime, f)
		}
		if got := VaultSaveMIME("img-009" + wantExt[mime]); got != mime {
			t.Errorf("VaultSaveMIME(%s) = %q, want %q", wantExt[mime], got, mime)
		}
	}
}

// A file that is not what its type says is refused before the sink, whichever
// way round the types are mixed up.
func TestVaultSave_AudioAndVideoBytesMustMatch(t *testing.T) {
	sink := &fakeVaultSink{}
	samples := avSamples()
	for mime := range samples {
		refusedInvalid(t, mime+" with PNG bytes", sink, vaultParams(map[string]any{"mime": mime}))
		refusedInvalid(t, mime+" with a script", sink, vaultParams(map[string]any{
			"mime": mime, "data": base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nrm -rf ~\n")),
		}))
	}
	refusedInvalid(t, "an MP3 labelled as WebM", sink, vaultParams(map[string]any{
		"mime": "video/webm", "data": base64.StdEncoding.EncodeToString(samples["audio/mpeg"]),
	}))
	refusedInvalid(t, "a PNG labelled as MP4", sink, vaultParams(map[string]any{"mime": "video/mp4"}))
	if sink.calls() != 0 {
		t.Errorf("%d mismatched files reached the sink", sink.calls())
	}
}
