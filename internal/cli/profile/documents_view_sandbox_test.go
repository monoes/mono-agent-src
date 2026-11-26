package profile

import "testing"

// The capture view carries summary.json's unsandboxed mark to the viewer.
func TestReadCaptureViewCarriesTheSummarysSandboxMark(t *testing.T) {
	t.Parallel()
	for body, want := range map[string]string{
		`{"status":"done","kind":"page","sandbox":"unsupported"}`: "unsupported",
		`{"status":"done","kind":"page"}`:                         "",
	} {
		v, err := readCaptureView(writeCapture(t, map[string]string{"meta.json": `{"title":"A page"}`, "summary.md": "x", "summary.json": body}))
		if err != nil {
			t.Fatal(err)
		}
		if v.SummaryStatus == nil || v.SummaryStatus.Sandbox != want {
			t.Errorf("%s: summary status = %+v, want sandbox %q", body, v.SummaryStatus, want)
		}
	}
}
