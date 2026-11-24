package operator

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// A one-time proof for an operator-only act that must run without a
// terminal (the app's `org sign --yes` is the first). The operator's own,
// interactive process issues a nonce bound to a purpose and a subject
// (IssueNonce); the act then presents it once (ConsumeNonce). Only the
// nonce's SHA-256 is kept on disk, so a process that reads the state
// directory learns nothing it could present, and a process that is
// not the operator's own terminal cannot issue one: an agent that clears its
// environment and has no terminal holds no nonce, so `--yes` alone proves
// nothing. Like the rest of this package it is a courtesy layer, not a sandbox
// (a process that can read the nonce from the operator's screen or terminal
// can use it); it makes the unmarked, terminal-less call stop being enough.

// NonceTTL is how long an issued nonce stays valid.
const NonceTTL = 10 * time.Minute

type nonceRecord struct {
	Purpose string `json:"purpose"`
	Subject string `json:"subject"`
	Expires int64  `json:"expires"`
}

func nonceDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".monoagent", "operator-nonces"), nil
}

func nonceFile(nonce string) (string, error) {
	dir, err := nonceDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(nonce))
	return filepath.Join(dir, hex.EncodeToString(sum[:])), nil
}

// IssueNonce returns a one-time nonce for purpose and subject. interactive
// says the caller is a person at a terminal or the app (not a piped or
// scripted run); a process that is not the operator's own (NotUser) or is not
// interactive is refused.
func IssueNonce(purpose, subject string, interactive bool) (string, error) {
	if purpose == "" || subject == "" {
		return "", errors.New("a nonce needs a purpose and a subject")
	}
	if why := NotUser(); why != "" {
		return "", fmt.Errorf("only the operator's own terminal issues a nonce: %s", why)
	}
	if !interactive {
		return "", errors.New("a nonce is issued from a terminal or the app, not from a piped or scripted run")
	}
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	nonce := hex.EncodeToString(raw[:])
	path, err := nonceFile(nonce)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	pruneNonces(filepath.Dir(path))
	body, _ := json.Marshal(nonceRecord{Purpose: purpose, Subject: subject, Expires: time.Now().Add(NonceTTL).Unix()})
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return "", err
	}
	return nonce, nil
}

// ConsumeNonce accepts nonce once, for exactly the purpose and subject it
// was issued for, before it expires. The file's removal decides single use:
// of two concurrent calls only the one whose Remove succeeds is accepted.
func ConsumeNonce(purpose, subject, nonce string) error {
	bad := errors.New("the one-time nonce is missing, expired, used or not for this act")
	if nonce == "" {
		return bad
	}
	path, err := nonceFile(nonce)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return bad
	}
	if err := os.Remove(path); err != nil {
		return bad
	}
	var rec nonceRecord
	if json.Unmarshal(body, &rec) != nil || time.Now().Unix() > rec.Expires {
		return bad
	}
	if subtle.ConstantTimeCompare([]byte(rec.Purpose), []byte(purpose)) != 1 ||
		subtle.ConstantTimeCompare([]byte(rec.Subject), []byte(subject)) != 1 {
		return bad
	}
	return nil
}

// pruneNonces removes expired nonce files; best effort.
func pruneNonces(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	now := time.Now().Unix()
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		var rec nonceRecord
		if b, err := os.ReadFile(p); err != nil || json.Unmarshal(b, &rec) != nil || now > rec.Expires {
			_ = os.Remove(p)
		}
	}
}
