package httpnodes

import (
	"context"
	"net"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

func TestIsInternalIP(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "::1", "10.1.2.3", "192.168.0.9", "172.16.0.1", "169.254.169.254", "100.64.1.1", "0.0.0.0", "fe80::1", "fd00::1"} {
		if !isInternalIP(net.ParseIP(s)) {
			t.Errorf("%s is not internal", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "93.184.216.34", "2606:4700:4700::1111"} {
		if isInternalIP(net.ParseIP(s)) {
			t.Errorf("%s is internal", s)
		}
	}
}

// An unapproved AI-authored workflow does not reach this machine's own services; an approved one does.
func TestRequestRefusesInternalAddressesWhenAIAuthored(t *testing.T) {
	rec := newRecorder(t, nil)
	cfg := map[string]interface{}{"url": rec.URL + "/admin", "method": "GET"}
	in := workflow.NodeInput{Items: []workflow.Item{{JSON: map[string]interface{}{}}}}

	out, err := (&RequestNode{}).Execute(workflow.WithAIAuthored(context.Background()), in, cfg)
	if err == nil {
		failed := false
		for _, o := range out {
			if o.Handle == "error" && len(o.Items) > 0 {
				failed = true
			}
		}
		if !failed {
			t.Fatalf("an unapproved AI-authored request reached loopback: %+v", out)
		}
	}
	rec.mu.Lock()
	n := len(rec.reqs)
	rec.mu.Unlock()
	if n != 0 {
		t.Fatalf("the loopback server saw %d requests", n)
	}

	if _, err := (&RequestNode{}).Execute(context.Background(), in, cfg); err != nil {
		t.Fatalf("an approved run: %v", err)
	}
	rec.last(t)
}
