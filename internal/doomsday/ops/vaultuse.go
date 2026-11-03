package ops

import (
	"bytes"
	"context"
	"errors"
	"io"
	"time"
)

// vault.use executor (note 13.4 section 5A). The agent receives exactly Envelope: an outcome from a fixed enum, the
// status code, the body length quantised into a finite number of buckets and a boolean the host computed from the
// body. The secret is injected at the transport by the host; no response byte, header, field or error text reaches
// the agent.

// Outcome is the fixed enum of what happened.
type Outcome string

const (
	OutcomeOK             Outcome = "ok"
	OutcomeTransportError Outcome = "transport-error"
	OutcomeTimeout        Outcome = "timeout"
	OutcomeMalformed      Outcome = "malformed"
	OutcomeRefused        Outcome = "refused"
)

// Envelope is everything an agent learns from a vault.use call.
type Envelope struct {
	Outcome      Outcome `json:"outcome"`
	Status       int     `json:"status"`
	LengthBucket int     `json:"length_bucket"`
	Predicate    *bool   `json:"predicate"`
}

// ErrMalformedResponse is what a Transport returns for a response it could not parse; it maps to OutcomeMalformed.
var ErrMalformedResponse = errors.New("malformed response")

// ErrCancelled: the call was cancelled (Stop, or the caller's context). Nothing is delivered.
var ErrCancelled = errors.New("doomsday ops: the call was cancelled, no result is delivered")

// Response is a transport's answer; the executor reads Body itself, capped.
type Response struct {
	Status int
	Body   io.ReadCloser
}

// Transport performs the request with the secret injected at req.Inject. It must not follow redirects, must use a
// pinned address set and must honour ctx.
type Transport interface {
	Do(ctx context.Context, req VaultArgs, secret []byte) (Response, error)
}

// SecretSource gives the host-side value of a named vault secret.
type SecretSource interface {
	Value(ctx context.Context, name string) ([]byte, error)
}

// Clock is the host's monotonic clock. Wait returns nil once Now() >= until, or ctx's error.
type Clock interface {
	Now() time.Time
	Wait(ctx context.Context, until time.Time) error
}

// VaultExecutor runs one approved vault.use call. Each approval carries exactly one transport attempt and the host
// never retries, so attempts of one operation are serialised by construction and the attempt budget (spent in
// Authorize, one unit per approval) counts every transport attempt. There is no ordering between operations.
type VaultExecutor struct {
	Clock     Clock
	Transport Transport
	Secrets   SecretSource
	Auditor   Auditor
}

type attempt struct {
	arrival time.Time // when the capped body read completed (or failed)
	status  int
	read    int64
	pred    *bool
	err     error
}

// Run performs the call. The deadline, the slot and the release are the charter's; the caller supplies none. The
// slot is derived from the instant the capped body read completed, never from the headers or the delivery instant.
func (x *VaultExecutor) Run(ctx context.Context, ap Authorization, req VaultArgs) (Envelope, error) {
	v := ap.vault
	if v == nil || ap.op != OpVaultUse {
		return Envelope{Outcome: OutcomeRefused}, nil
	}
	secret, err := x.Secrets.Value(ctx, req.Secret)
	if err != nil {
		return Envelope{Outcome: OutcomeRefused}, nil
	}
	t0 := x.Clock.Now()
	slot := time.Duration(v.SlotMS) * time.Millisecond
	maxCall := time.Duration(v.MaxCallMS) * time.Millisecond
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan attempt, 1)
	go func() { done <- x.exchange(cctx, v, req, secret) }()
	timeout := make(chan struct{})
	go func() {
		if x.Clock.Wait(cctx, t0.Add(maxCall)) == nil {
			close(timeout)
		}
	}()
	var a attempt
	select {
	case a = <-done:
	case <-timeout:
		cancel()
		a = attempt{arrival: t0.Add(maxCall), err: context.DeadlineExceeded}
	case <-ctx.Done():
		x.audit(ctx, ap, "cancelled")
		return Envelope{}, ErrCancelled
	}
	elapsed := a.arrival.Sub(t0)
	n := int64((elapsed + slot - 1) / slot)
	if elapsed >= maxCall {
		n, a.err = v.Slots(), context.DeadlineExceeded
	}
	n = min(max(n, 1), v.Slots())
	env := envelope(v, a)
	if err := x.Clock.Wait(ctx, t0.Add(time.Duration(n)*slot)); err != nil {
		x.audit(ctx, ap, "cancelled")
		return Envelope{}, ErrCancelled
	}
	x.audit(ctx, ap, string(env.Outcome))
	return env, nil
}

func (x *VaultExecutor) audit(ctx context.Context, ap Authorization, result string) {
	if x.Auditor != nil {
		_ = x.Auditor.Append(context.WithoutCancel(ctx), AuditRow{Op: OpVaultUse, ApprovalID: ap.id, ArgsHash: ap.hash, Result: "vault.attempt:" + result})
	}
}

// exchange makes the request and reads at most the cap of the body, then stops. The predicate is evaluated while
// reading, over a window of marker-length bytes, so memory does not grow with the body.
func (x *VaultExecutor) exchange(ctx context.Context, v *VaultUse, req VaultArgs, secret []byte) attempt {
	resp, err := x.Transport.Do(ctx, req, secret)
	if err != nil {
		return attempt{arrival: x.Clock.Now(), err: err}
	}
	a := attempt{status: resp.Status}
	defer resp.Body.Close()
	marker := []byte(v.Predicate)
	var hit bool
	var tail []byte
	buf := make([]byte, 4096)
	for a.read < v.Cap() {
		n, rerr := resp.Body.Read(buf[:min(int64(len(buf)), v.Cap()-a.read)])
		if n > 0 {
			a.read += int64(n)
			if len(marker) > 0 && !hit {
				window := append(tail, buf[:n]...)
				hit = bytes.Contains(window, marker)
				tail = append(tail[:0], window[max(0, len(window)-len(marker)+1):]...)
			}
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				a.err = rerr
			}
			break
		}
	}
	if len(marker) > 0 && a.err == nil {
		a.pred = &hit
	}
	a.arrival = x.Clock.Now()
	return a
}

// envelope maps an attempt to the fixed envelope. No error text survives.
func envelope(v *VaultUse, a attempt) Envelope {
	switch {
	case errors.Is(a.err, context.DeadlineExceeded):
		return Envelope{Outcome: OutcomeTimeout}
	case errors.Is(a.err, ErrMalformedResponse) || (a.err == nil && (a.status < 100 || a.status > 599)):
		return Envelope{Outcome: OutcomeMalformed}
	case a.err != nil:
		return Envelope{Outcome: OutcomeTransportError, Status: max(0, a.status)}
	}
	return Envelope{Outcome: OutcomeOK, Status: a.status, LengthBucket: int(LengthBucket(a.read, v.BucketSize, v.BucketCount)), Predicate: a.pred}
}

// LengthBucket is min(ceil(read / size), count): zero for an empty body, saturating at count.
func LengthBucket(read, size, count int64) int64 {
	if read <= 0 {
		return 0
	}
	return min((read+size-1)/size, count)
}
