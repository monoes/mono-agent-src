package openaiapi

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"
)

// imageWriteBudget is how long a client has to read the images once they are ready. The
// slot is given back by then and the images are held in memory until the last byte is
// written, so a client that does not read must not hold them for as long as the
// connection was given for the turn. A variable so a test can shorten it.
var imageWriteBudget = 2 * time.Minute

// writeImages writes the answer of POST /v1/images/generations,
// {"created":<unix time>,"data":[{"b64_json":"<base64 of an image>"},…]}, with the
// base64 of each image going straight to the connection as it is encoded: the body is a
// third more than the images, and building it first would hold the images, the body and
// a copy of it at once.
func writeImages(w http.ResponseWriter, created int64, images [][]byte) {
	writeImagesWith(w, "", created, images)
}

// writeImagesWith is writeImages with head, JSON members each followed by a comma, in
// front of "created": the answer of a finished image job names the job.
func writeImagesWith(w http.ResponseWriter, head string, created int64, images [][]byte) {
	extendWriteDeadline(w, imageWriteBudget)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	out := bufio.NewWriterSize(w, 64<<10) // a write error is kept, and ends everything after it
	fmt.Fprintf(out, `{%s"created":%d,"data":[`, head, created)
	for i, img := range images {
		if i > 0 {
			out.WriteByte(',')
		}
		out.WriteString(`{"b64_json":"`)
		enc := base64.NewEncoder(base64.StdEncoding, out)
		enc.Write(img)
		enc.Close()
		out.WriteString(`"}`)
	}
	out.WriteString("]}\n")
	_ = out.Flush()
}
