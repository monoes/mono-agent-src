package workflow

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rs/zerolog"
)

// handshakeServer registers one POST webhook (auth header required, so a
// handshake answered before the auth check is visible) and counts runs.
func handshakeServer(t *testing.T, mutate func(*WebhookRegistration)) (*WebhookServer, *int32) {
	t.Helper()
	s := NewWebhookServer(":0", zerolog.Nop())
	var fired int32
	reg := &WebhookRegistration{
		WorkflowID: "wf1", NodeID: "n1", Path: "hs", Method: "POST",
		AuthHeader: "X-Token", AuthToken: "t",
		TriggerFn: func([]Item) { atomic.AddInt32(&fired, 1) },
	}
	if mutate != nil {
		mutate(reg)
	}
	if err := s.Register(reg); err != nil {
		t.Fatal(err)
	}
	return s, &fired
}

func serve(s *WebhookServer, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, r)
	return rec
}

func TestHandshakeHead(t *testing.T) {
	s, fired := handshakeServer(t, nil)
	rec := serve(s, httptest.NewRequest(http.MethodHead, "/webhook/hs", nil))
	if rec.Code != 200 || rec.Body.Len() != 0 || *fired != 0 {
		t.Fatalf("HEAD: code %d body %q fired %d", rec.Code, rec.Body.String(), *fired)
	}
	if rec := serve(s, httptest.NewRequest(http.MethodHead, "/webhook/nope", nil)); rec.Code != 404 {
		t.Fatalf("HEAD unknown path: %d, want 404", rec.Code)
	}
}

func TestHandshakeHubChallenge(t *testing.T) {
	s, fired := handshakeServer(t, nil)
	rec := serve(s, httptest.NewRequest(http.MethodGet, "/webhook/hs?hub.mode=subscribe&hub.challenge=abc123", nil))
	if rec.Code != 200 || rec.Body.String() != "abc123" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("hub.challenge: %d %q %q", rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
	}
	// Not the shape (no hub.mode): the ordinary method check applies.
	if rec := serve(s, httptest.NewRequest(http.MethodGet, "/webhook/hs?hub.challenge=abc", nil)); rec.Code != 405 {
		t.Fatalf("GET without hub.mode: %d, want 405", rec.Code)
	}
	if *fired != 0 {
		t.Fatalf("a handshake started %d runs", *fired)
	}
}

func TestHandshakeHubVerifyToken(t *testing.T) {
	s, _ := handshakeServer(t, func(r *WebhookRegistration) { r.VerifyToken = "sekret" })
	rec := serve(s, httptest.NewRequest(http.MethodGet, "/webhook/hs?hub.mode=subscribe&hub.challenge=x&hub.verify_token=wrong", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("wrong token: %d, want 403", rec.Code)
	}
	rec = serve(s, httptest.NewRequest(http.MethodGet, "/webhook/hs?hub.mode=subscribe&hub.challenge=x&hub.verify_token=sekret", nil))
	if rec.Code != 200 || rec.Body.String() != "x" {
		t.Fatalf("right token: %d %q", rec.Code, rec.Body.String())
	}
}

func TestHandshakeGraphValidationToken(t *testing.T) {
	s, fired := handshakeServer(t, nil)
	rec := serve(s, httptest.NewRequest(http.MethodPost, "/webhook/hs?validationToken=Validation%3A+Testing", nil))
	if rec.Code != 200 || rec.Body.String() != "Validation: Testing" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("validationToken: %d %q", rec.Code, rec.Body.String())
	}
	if *fired != 0 {
		t.Fatal("validationToken started a run")
	}
}

func TestHandshakeXHookSecret(t *testing.T) {
	for _, withDB := range []bool{false, true} {
		s, fired := handshakeServer(t, func(r *WebhookRegistration) { r.AuthHeader = "" })
		if withDB {
			s.SetStateDB(newStateDB(t))
		}
		req := httptest.NewRequest(http.MethodPost, "/webhook/hs", nil)
		req.Header.Set("X-Hook-Secret", "asana-secret")
		rec := serve(s, req)
		if rec.Code != 200 || rec.Header().Get("X-Hook-Secret") != "asana-secret" || *fired != 0 {
			t.Fatalf("db=%v: handshake %d %q fired %d", withDB, rec.Code, rec.Header().Get("X-Hook-Secret"), *fired)
		}

		body := `{"events":[]}`
		bad := httptest.NewRequest(http.MethodPost, "/webhook/hs", strings.NewReader(body))
		bad.Header.Set("X-Hook-Signature", "00ff")
		if rec := serve(s, bad); rec.Code != http.StatusUnauthorized {
			t.Fatalf("db=%v: bad signature: %d, want 401", withDB, rec.Code)
		}
		mac := hmac.New(sha256.New, []byte("asana-secret"))
		mac.Write([]byte(body))
		good := httptest.NewRequest(http.MethodPost, "/webhook/hs", strings.NewReader(body))
		good.Header.Set("X-Hook-Signature", hex.EncodeToString(mac.Sum(nil)))
		if rec := serve(s, good); rec.Code != 200 || *fired != 1 {
			t.Fatalf("db=%v: good signature: %d fired %d", withDB, rec.Code, *fired)
		}
	}
}

func TestHandshakeSlackURLVerification(t *testing.T) {
	s, fired := handshakeServer(t, nil)
	req := httptest.NewRequest(http.MethodPost, "/webhook/hs",
		strings.NewReader(`{"token":"x","challenge":"3eZbrw1aB","type":"url_verification"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := serve(s, req)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"challenge":"3eZbrw1aB"}` {
		t.Fatalf("slack: %d %q", rec.Code, rec.Body.String())
	}
	// An event_callback is a delivery: the auth check still applies.
	req = httptest.NewRequest(http.MethodPost, "/webhook/hs", strings.NewReader(`{"type":"event_callback","challenge":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	if rec := serve(s, req); rec.Code != http.StatusUnauthorized || *fired != 0 {
		t.Fatalf("event_callback: %d fired %d", rec.Code, *fired)
	}
}

func TestHandshakeSNSSubscriptionConfirmation(t *testing.T) {
	var visits int32
	aws := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("Action") == "ConfirmSubscription" {
			atomic.AddInt32(&visits, 1)
		}
	}))
	defer aws.Close()

	s, fired := handshakeServer(t, nil)
	s.handshakes.client = aws.Client()
	s.handshakes.allowSNSHost = func(host string) bool { return host == "127.0.0.1" }

	send := func(subscribeURL string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/webhook/hs",
			strings.NewReader(`{"Type":"SubscriptionConfirmation","SubscribeURL":"`+subscribeURL+`"}`))
		req.Header.Set("Content-Type", "text/plain; charset=UTF-8")
		req.Header.Set("x-amz-sns-message-type", "SubscriptionConfirmation")
		return serve(s, req)
	}
	if rec := send(aws.URL + "/?Action=ConfirmSubscription&Token=t"); rec.Code != 200 || visits != 1 {
		t.Fatalf("confirm: %d visits %d body %s", rec.Code, visits, rec.Body.String())
	}
	if rec := send(strings.Replace(aws.URL, "https", "http", 1) + "/?Action=ConfirmSubscription"); rec.Code != 400 || visits != 1 {
		t.Fatalf("http URL: %d visits %d", rec.Code, visits)
	}
	if *fired != 0 {
		t.Fatal("SNS confirmation started a run")
	}
}

func TestIsSNSHost(t *testing.T) {
	for host, want := range map[string]bool{
		"sns.us-east-1.amazonaws.com": true,
		"amazonaws.com.evil.example":  false,
		"evilamazonaws.com":           false,
		"127.0.0.1":                   false,
	} {
		if got := isSNSHost(host); got != want {
			t.Errorf("isSNSHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestHandshakeStravaJSONChallenge(t *testing.T) {
	s, fired := handshakeServer(t, func(r *WebhookRegistration) { r.VerifyToken = "strava"; r.ChallengeJSON = true })
	rec := serve(s, httptest.NewRequest(http.MethodGet, "/webhook/hs?hub.mode=subscribe&hub.challenge=15f7d1a91c1f40f8a748fd134752feb3&hub.verify_token=strava", nil))
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"hub.challenge":"15f7d1a91c1f40f8a748fd134752feb3"}` ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("strava: %d %q %q", rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
	}
	if rec := serve(s, httptest.NewRequest(http.MethodGet, "/webhook/hs?hub.mode=subscribe&hub.challenge=x&hub.verify_token=no", nil)); rec.Code != http.StatusForbidden {
		t.Fatalf("strava wrong token: %d, want 403", rec.Code)
	}
	if *fired != 0 {
		t.Fatal("strava check started a run")
	}
}

func TestHandshakeOnfleetCheck(t *testing.T) {
	s, fired := handshakeServer(t, nil)
	rec := serve(s, httptest.NewRequest(http.MethodGet, "/webhook/hs?check=a1b2c3", nil))
	if rec.Code != 200 || rec.Body.String() != "a1b2c3" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("onfleet: %d %q %q", rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
	}
	// Other parameters next to check: not the shape.
	if rec := serve(s, httptest.NewRequest(http.MethodGet, "/webhook/hs?check=a&x=1", nil)); rec.Code != 405 {
		t.Fatalf("check with another parameter: %d, want 405", rec.Code)
	}
	if *fired != 0 {
		t.Fatal("onfleet check started a run")
	}
}

func TestHandshakeMailchimpBareGet(t *testing.T) {
	s, fired := handshakeServer(t, nil)
	if rec := serve(s, httptest.NewRequest(http.MethodGet, "/webhook/hs", nil)); rec.Code != 405 {
		t.Fatalf("bare GET without answer_get: %d, want 405", rec.Code)
	}
	if *fired != 0 {
		t.Fatal("refused bare GET started a run")
	}
	s, fired = handshakeServer(t, func(r *WebhookRegistration) { r.AnswerGet = true })
	rec := serve(s, httptest.NewRequest(http.MethodGet, "/webhook/hs", nil))
	if rec.Code != 200 || rec.Body.Len() != 0 {
		t.Fatalf("mailchimp: %d %q", rec.Code, rec.Body.String())
	}
	// A GET with a query is not the probe.
	if rec := serve(s, httptest.NewRequest(http.MethodGet, "/webhook/hs?a=1", nil)); rec.Code != 405 {
		t.Fatalf("GET with query: %d, want 405", rec.Code)
	}
	// Deliveries still need the auth header and start runs.
	req := httptest.NewRequest(http.MethodPost, "/webhook/hs", strings.NewReader(`{"type":"subscribe"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Token", "t")
	if rec := serve(s, req); rec.Code != 200 || *fired != 1 {
		t.Fatalf("delivery: %d fired %d", rec.Code, *fired)
	}
}

func TestWebhookOptionsURLChecks(t *testing.T) {
	reg := &WebhookRegistration{}
	if err := applyWebhookOptions(reg, map[string]interface{}{"challenge_format": "json", "answer_get": "true"}); err != nil {
		t.Fatal(err)
	}
	if !reg.ChallengeJSON || !reg.AnswerGet {
		t.Fatalf("options not read: %+v", reg)
	}
	if err := applyWebhookOptions(&WebhookRegistration{}, map[string]interface{}{"challenge_format": "xml"}); err == nil {
		t.Fatal("challenge_format xml accepted")
	}
}
