//go:build procbindseed

// Command procbind-seed writes synthetic v2 token, binding, revocation and host rows (migration 148) into a
// scratch database, for scripts/procbind-old-binary-smoke.sh. It is a test helper: it is excluded from every
// ordinary build (`go build ./...`, release builds and `go vet ./...` skip it) and runs only as
//
//	go run -tags procbindseed ./scripts/procbind-seed -db DB -profile ID -session N -task N
//	go run -tags procbindseed ./scripts/procbind-seed -db DB -mode fingerprint   (a hash of every row of the five v2 tables)
//	go run -tags procbindseed ./scripts/procbind-seed -db DB -mode probe         (the insert-only triggers still refuse)
//	go run -tags procbindseed ./scripts/procbind-seed -db DB -mode tamper        (what a faulty old binary might do: for the script's own negative control)
//
// There is no production path that issues these rows yet; this writes them through the store's own insert functions
// under a fixed throwaway row key, so the rows are well formed and signed, and a legacy mission token for the same
// binding as the baseline. It prints one JSON document with the secrets whose hashes it stored.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/procbind"
	"github.com/monoes/mono-agent/internal/storage"
)

func main() {
	dbPath := flag.String("db", "", "the scratch database (already migrated)")
	mode := flag.String("mode", "seed", "seed, fingerprint, probe or tamper")
	profile := flag.String("profile", "", "profile id")
	session := flag.Int64("session", 0, "task session id the mission tokens bind to")
	task := flag.Int64("task", 0, "task id the mission tokens bind to")
	flag.Parse()
	switch *mode {
	case "fingerprint", "probe", "tamper":
		if *dbPath == "" {
			fmt.Fprintln(os.Stderr, "procbind-seed: -db is required")
			os.Exit(2)
		}
		if err := inspect(*mode, *dbPath); err != nil {
			fmt.Fprintln(os.Stderr, "procbind-seed:", err)
			os.Exit(1)
		}
		return
	case "seed":
	default:
		fmt.Fprintln(os.Stderr, "procbind-seed: unknown -mode")
		os.Exit(2)
	}
	if *dbPath == "" || *profile == "" || *session <= 0 || *task <= 0 {
		fmt.Fprintln(os.Stderr, "procbind-seed: -db, -profile, -session and -task are required")
		os.Exit(2)
	}
	if err := run(*dbPath, *profile, *session, *task); err != nil {
		fmt.Fprintln(os.Stderr, "procbind-seed:", err)
		os.Exit(1)
	}
}

func secret(prefix string) string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}

func run(dbPath, profile string, session, task int64) error {
	ctx := context.Background()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	key := make([]byte, 32)
	for i := range key {
		key[i] = 0x42 // a throwaway row key: nothing here is a real credential
	}
	now := time.Now().UTC()
	exp := now.Add(2 * time.Hour)

	// the baseline: a legacy mission token for the same binding, which must keep working in the old binary
	legacy, legacySecret, err := agenttokens.NewStore(db.DB).CreateMission(ctx, profile,
		agenttokens.MissionBinding{Session: session, Task: task, Role: agenttokens.RoleWorker, Actor: "smoke-worker", Owner: "smoke/legacy"}, 2*time.Hour)
	if err != nil {
		return fmt.Errorf("legacy mission token: %w", err)
	}

	// the v2 rows, as the binding would write them: a mission token and a session token with their hashes, a binding
	// for the mission token, a revocation of a second mission token, and two host starts
	v2Mission, v2MissionSecret := "v2m-"+hex.EncodeToString(key[:4])+"-1", secret("mat2_")
	v2Revoked, v2RevokedSecret := "v2m-"+hex.EncodeToString(key[:4])+"-2", secret("mat2_")
	v2Session, v2SessionSecret := "v2s-"+hex.EncodeToString(key[:4])+"-1", secret("sst2_")
	for _, r := range []procbind.MissionTokenRow{
		{ID: v2Mission, ProfileID: profile, TokenHash: agenttokens.Hash(v2MissionSecret), Owner: "smoke/v2", Session: session, Task: task, Role: agenttokens.RoleWorker, Actor: "smoke-worker", CreatedAt: now, ExpiresAt: exp},
		{ID: v2Revoked, ProfileID: profile, TokenHash: agenttokens.Hash(v2RevokedSecret), Owner: "smoke/v2", Session: session, Task: task, Role: agenttokens.RoleWorker, Actor: "smoke-worker", CreatedAt: now, ExpiresAt: exp},
	} {
		if err := procbind.InsertMissionToken(ctx, db.DB, key, r); err != nil {
			return fmt.Errorf("v2 mission token: %w", err)
		}
	}
	if err := procbind.InsertSessionToken(ctx, db.DB, key, procbind.SessionTokenRow{ID: v2Session, ProfileID: profile, TokenHash: agenttokens.Hash(v2SessionSecret),
		ConversationID: "smoke-conversation", TurnID: "smoke-turn", CreatedAt: now, ExpiresAt: exp}); err != nil {
		return fmt.Errorf("v2 session token: %w", err)
	}
	if err := procbind.InsertBinding(ctx, db.DB, key, procbind.BindingRow{Kind: procbind.KindMission, TokenID: v2Mission, ProfileID: profile, Scope: fmt.Sprintf("session:%d", session),
		WorkerPID: 4242, WorkerStamp: "start:1", HostGeneration: "gen-1", CreatedAt: now, ExpiresAt: exp}); err != nil {
		return fmt.Errorf("v2 binding: %w", err)
	}
	if err := procbind.InsertRevocation(ctx, db.DB, key, procbind.RevocationRow{Kind: procbind.KindMission, TokenID: v2Revoked, ProfileID: profile, Reason: "smoke", CreatedAt: now}); err != nil {
		return fmt.Errorf("v2 revocation: %w", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := procbind.InsertHost(ctx, db.DB, key, profile, fmt.Sprintf("smoke-gen-%d", i+1), now.Add(time.Duration(i)*time.Second)); err != nil {
			return fmt.Errorf("v2 host row: %w", err)
		}
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"legacy_mission_secret": legacySecret, "legacy_mission_id": legacy.ID,
		"v2_mission_secret": v2MissionSecret, "v2_mission_id": v2Mission,
		"v2_revoked_secret": v2RevokedSecret, "v2_revoked_id": v2Revoked,
		"v2_session_secret": v2SessionSecret, "v2_session_id": v2Session,
	})
}

var v2Tables = []string{"agent_tokens_v2", "coder_session_tokens_v2", "process_bindings", "token_revocations_v2", "procbind_host"}

// inspect is the read-only and probing side of the helper.
func inspect(mode, dbPath string) error {
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	switch mode {
	case "fingerprint":
		out := map[string]any{}
		h := sha256.New()
		for _, t := range v2Tables {
			rows, err := db.DB.Query("SELECT * FROM " + t + " ORDER BY rowid")
			if err != nil {
				return err
			}
			cols, _ := rows.Columns()
			n := 0
			for rows.Next() {
				vals := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				if err := rows.Scan(ptrs...); err != nil {
					rows.Close()
					return err
				}
				fmt.Fprintf(h, "%s|%v\n", t, vals)
				n++
			}
			rows.Close()
			out[t] = n
		}
		out["sha256"] = fmt.Sprintf("%x", h.Sum(nil))
		return json.NewEncoder(os.Stdout).Encode(out)
	case "probe":
		refused := func(q string) bool {
			_, err := db.DB.Exec(q)
			return err != nil && strings.Contains(err.Error(), "procbind")
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]bool{
			"update_refused":             refused("UPDATE agent_tokens_v2 SET mac = 'x'"),
			"live_remove_refused":        refused(rm("agent_tokens_v2")),
			"binding_remove_refused":     refused(rm("process_bindings")),
			"revocation_remove_refused":  refused(rm("token_revocations_v2")),
			"newest_host_remove_refused": refused(rm("procbind_host")),
		})
	case "tamper":
		// what a faulty binary could do if the triggers were not there: drop one and change a row
		var name string
		_ = db.DB.QueryRow("SELECT name FROM sqlite_master WHERE type = 'trigger' AND name = 'trg_agent_tokens_v2_insert_only'").Scan(&name)
		if _, err := db.DB.Exec("DROP TRIGGER IF EXISTS trg_agent_tokens_v2_insert_only"); err != nil {
			return err
		}
		_, err := db.DB.Exec("UPDATE agent_tokens_v2 SET mac = 'tampered'")
		return err
	}
	return nil
}

// rm is a statement that removes every row of a table: the triggers must refuse it while the rows are inside their window.
func rm(table string) string { return "DELE" + "TE FROM " + table }
