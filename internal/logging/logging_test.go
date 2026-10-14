package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

// initFor runs Init with env and args, writing to a buffer, and restores the
// globals Init changes when the test ends.
func initFor(t *testing.T, args []string, env map[string]string) *bytes.Buffer {
	t.Helper()
	prevDefault, prevOut, prevFlags, prevLevel, prevChosen := slog.Default(), log.Writer(), log.Flags(), Level(), LevelChosen()
	prevEnvLevel, hadEnvLevel := os.LookupEnv(EnvLevel)
	t.Cleanup(func() {
		slog.SetDefault(prevDefault)
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
		SetLevel(prevLevel)
		levelChosen.Store(prevChosen)
		if hadEnvLevel {
			os.Setenv(EnvLevel, prevEnvLevel)
		} else {
			os.Unsetenv(EnvLevel)
		}
	})
	var buf bytes.Buffer
	Init(args, func(k string) string { return env[k] }, &buf)
	return &buf
}

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{
		"": DefaultLevel, "debug": slog.LevelDebug, "INFO": slog.LevelInfo,
		" warn ": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError,
	} {
		if got, err := ParseLevel(in); err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if got, err := ParseLevel("loud"); err == nil || got != DefaultLevel {
		t.Errorf("ParseLevel(loud) = %v, %v; want the default and an error", got, err)
	}
}

func TestLevelFlag(t *testing.T) {
	cases := []struct {
		args []string
		want string
		ok   bool
	}{
		{[]string{"workflow", "run", "--log-level", "debug", "x"}, "debug", true},
		{[]string{"--log-level=info", "doctor"}, "info", true},
		{[]string{"doctor", "--json"}, "", false},
		{[]string{"node", "run", "--", "--log-level", "debug"}, "", false},
		{[]string{"--log-level"}, "", false},
	}
	for _, c := range cases {
		got, ok := LevelFlag(c.args)
		if got != c.want || ok != c.ok {
			t.Errorf("LevelFlag(%q) = %q, %v; want %q, %v", c.args, got, ok, c.want, c.ok)
		}
	}
}

func TestInitLevelFlagBeatsEnvironment(t *testing.T) {
	initFor(t, []string{"--log-level", "error"}, map[string]string{EnvLevel: "debug"})
	if Level() != slog.LevelError {
		t.Fatalf("level = %v, want ERROR from the flag", Level())
	}
	if os.Getenv(EnvLevel) != "error" {
		t.Errorf("%s = %q; the flag's level is exported for child processes", EnvLevel, os.Getenv(EnvLevel))
	}
	initFor(t, nil, map[string]string{EnvLevel: "debug"})
	if Level() != slog.LevelDebug {
		t.Fatalf("level = %v, want DEBUG from the environment", Level())
	}
}

func TestLevelChosen(t *testing.T) {
	for _, tc := range []struct {
		args []string
		env  map[string]string
		want bool
	}{
		{nil, nil, false},
		{[]string{"--log-level=warn"}, nil, true},
		{nil, map[string]string{EnvLevel: "warn"}, true},
		{[]string{"--log-level=loud"}, nil, false},
	} {
		initFor(t, tc.args, tc.env)
		if LevelChosen() != tc.want {
			t.Errorf("args %v env %v: LevelChosen = %v, want %v", tc.args, tc.env, LevelChosen(), tc.want)
		}
	}
}

func TestInitLogFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cli.log")
	if err := os.WriteFile(path, []byte("earlier\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	buf := initFor(t, []string{"workflow", "run", "x", "--log-file", path}, nil)
	slog.Info("hidden at the default level")
	slog.Warn("to the file")
	if buf.Len() != 0 {
		t.Errorf("--log-file still wrote to stderr: %q", buf.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); !strings.HasPrefix(got, "earlier\n") || !strings.Contains(got, `level=WARN msg="to the file"`) || strings.Contains(got, "hidden") {
		t.Errorf("log file = %q", got)
	}

	// A file that cannot be opened: stderr, with a warning.
	buf = initFor(t, []string{"--log-file=" + filepath.Join(t.TempDir(), "no", "such", "dir", "x.log")}, nil)
	slog.Warn("still logged")
	if out := buf.String(); !strings.Contains(out, "--log-file:") || !strings.Contains(out, "still logged") {
		t.Errorf("unopenable --log-file: %q", out)
	}
}

func TestInitUnknownLevelWarnsAndKeepsDefault(t *testing.T) {
	buf := initFor(t, []string{"--log-level=loud"}, nil)
	if Level() != DefaultLevel {
		t.Fatalf("level = %v, want the default", Level())
	}
	if !strings.Contains(buf.String(), "unknown log level") {
		t.Errorf("no warning about the level: %q", buf.String())
	}
}

// The standard log package's unmarked lines (migrations, reconcile notes)
// are INFO, hidden at the default level; marked lines keep their level.
func TestStdLogBridgeDefaultHidesInfo(t *testing.T) {
	buf := initFor(t, nil, nil)
	log.Printf("applied migration %03d (%s)", 61, "061_x.sql")
	log.Printf("migrated %d legacy action(s)", 0)
	log.Printf("storage: reconcile: created per-profile vault_keys table")
	if buf.Len() != 0 {
		t.Fatalf("info lines shown at the default level: %q", buf.String())
	}
	log.Printf("WARN: schema_migrations version %d recorded as %q", 7, "x")
	log.Printf("storage: reconcile: WARN: vault_keys lacks profile_id")
	log.Println("WARNING: http.ssh: host key verification is disabled")
	log.Printf("ERROR: boom")
	out := buf.String()
	for _, want := range []string{
		`level=WARN msg="schema_migrations version 7 recorded as \"x\""`,
		`level=WARN msg="storage: reconcile: WARN: vault_keys lacks profile_id"`,
		`level=WARN msg="http.ssh: host key verification is disabled"`,
		`level=ERROR msg=boom`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in\n%s", want, out)
		}
	}
}

func TestStdLogBridgeInfoLevelShowsMigrations(t *testing.T) {
	buf := initFor(t, []string{"--log-level=info"}, nil)
	log.Printf("applied migration %03d (%s)", 61, "061_x.sql")
	if !strings.Contains(buf.String(), `level=INFO msg="applied migration 061 (061_x.sql)"`) {
		t.Errorf("migration line not shown at info: %q", buf.String())
	}
}

func TestStdLevel(t *testing.T) {
	cases := []struct {
		in, msg string
		level   slog.Level
	}{
		{"applied migration 001 (a.sql)", "applied migration 001 (a.sql)", slog.LevelInfo},
		{"WARN: x", "x", slog.LevelWarn},
		{"WARN x", "x", slog.LevelWarn},
		{"a: b: ERROR: c", "a: b: ERROR: c", slog.LevelError},
		{"WARNINGS are fine", "WARNINGS are fine", slog.LevelInfo},
		{"automation: Warn later: WARN", "automation: Warn later: WARN", slog.LevelWarn},
		{"", "", slog.LevelInfo},
	}
	for _, c := range cases {
		level, msg := stdLevel(c.in)
		if level != c.level || msg != c.msg {
			t.Errorf("stdLevel(%q) = %v, %q; want %v, %q", c.in, level, msg, c.level, c.msg)
		}
	}
}

func TestJSONFormatAndRunIDFromEnvironment(t *testing.T) {
	buf := initFor(t, nil, map[string]string{EnvFormat: "json", EnvRunID: "exec-1"})
	slog.Warn("hello", "k", 1)
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("not JSON: %q: %v", buf.String(), err)
	}
	if rec["msg"] != "hello" || rec["level"] != "WARN" || rec[KeyRunID] != "exec-1" {
		t.Errorf("record = %v", rec)
	}
}

func TestZerologBridge(t *testing.T) {
	buf := initFor(t, nil, nil)
	zl := Zerolog().With().Str("component", "extension").Logger()
	zl.Info().Msg("connected")
	if buf.Len() != 0 {
		t.Fatalf("info event shown at the default level: %q", buf.String())
	}
	zl.Warn().Int("port", 9223).Msg("fallback port")
	out := buf.String()
	if !strings.Contains(out, `level=WARN msg="fallback port" component=extension port=9223`) {
		t.Errorf("zerolog warning not bridged: %q", out)
	}
	if strings.Contains(out, "{") {
		t.Errorf("raw zerolog JSON reached the output: %q", out)
	}

	// A caller lowering the zerolog level (--verbose) still gets its events.
	buf.Reset()
	vl := Zerolog().Level(zerolog.DebugLevel)
	vl.Debug().Msg("verbose")
	if !strings.Contains(buf.String(), "level=DEBUG msg=verbose") {
		t.Errorf("lowered zerolog level dropped: %q", buf.String())
	}
}

// Demoted events drop to their new level and follow the root level; other
// events keep theirs.
func TestZerologDemote(t *testing.T) {
	t.Cleanup(Demote(slog.LevelInfo, "node failed"))
	buf := initFor(t, nil, nil)
	zl := Zerolog()
	zl.Error().Str("node_id", "a").Msg("node failed")
	if buf.Len() != 0 {
		t.Fatalf("demoted event shown at the default level: %q", buf.String())
	}
	zl.Error().Msg("other failure")
	if !strings.Contains(buf.String(), `level=ERROR msg="other failure"`) {
		t.Errorf("undemoted event lost: %q", buf.String())
	}

	buf = initFor(t, []string{"--log-level=info"}, nil)
	zl = Zerolog()
	zl.Error().Msg("node failed")
	if !strings.Contains(buf.String(), `level=INFO msg="node failed"`) {
		t.Errorf("demoted event not shown at info: %q", buf.String())
	}
}

func TestZerologFollowsRootLevel(t *testing.T) {
	initFor(t, []string{"--log-level=debug"}, nil)
	if got := Zerolog().GetLevel(); got != zerolog.DebugLevel {
		t.Errorf("zerolog level = %v at debug", got)
	}
	initFor(t, nil, nil)
	if got := Zerolog().GetLevel(); got != zerolog.WarnLevel {
		t.Errorf("zerolog level = %v at the default", got)
	}
}

func TestRunIDContext(t *testing.T) {
	t.Setenv(EnvRunID, "")
	buf := initFor(t, nil, nil)
	ctx := WithRunID(context.Background(), "exec-42")
	if RunID(ctx) != "exec-42" {
		t.Fatalf("RunID = %q", RunID(ctx))
	}
	FromContext(ctx).Warn("node failed")
	if !strings.Contains(buf.String(), "run_id=exec-42") {
		t.Errorf("run_id missing: %q", buf.String())
	}
	buf.Reset()
	zc := ZerologContext(ctx)
	zc.Warn().Msg("z")
	if !strings.Contains(buf.String(), "run_id=exec-42") {
		t.Errorf("run_id missing from the zerolog bridge: %q", buf.String())
	}

	env := ChildEnv(ctx, []string{"PATH=/bin", EnvRunID + "=old"})
	if strings.Join(env, " ") != "PATH=/bin "+EnvRunID+"=exec-42" {
		t.Errorf("ChildEnv = %q", env)
	}
	if got := ChildEnv(context.Background(), []string{"A=1"}); len(got) != 1 {
		t.Errorf("ChildEnv without a run id = %q", got)
	}
	t.Setenv(EnvRunID, "parent-run")
	if RunID(context.Background()) != "parent-run" {
		t.Errorf("RunID does not fall back to %s", EnvRunID)
	}
}

func TestRotate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	write := func(p string, n int) {
		t.Helper()
		if err := os.WriteFile(p, bytes.Repeat([]byte("x"), n), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if rotated, err := Rotate(path, 10, 3); rotated || err != nil {
		t.Fatalf("missing file: rotated=%v err=%v", rotated, err)
	}
	write(path, 10)
	if rotated, _ := Rotate(path, 10, 3); rotated {
		t.Fatal("rotated a file at the limit")
	}
	// Four rotations: the oldest falls off, three are kept.
	for i := 1; i <= 4; i++ {
		write(path, 10+i)
		if rotated, err := Rotate(path, 10, 3); !rotated || err != nil {
			t.Fatalf("rotation %d: rotated=%v err=%v", i, rotated, err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("rotation %d left %s", i, path)
		}
	}
	for n, size := range map[string]int64{".1": 14, ".2": 13, ".3": 12} {
		fi, err := os.Stat(path + n)
		if err != nil || fi.Size() != size {
			t.Errorf("%s: %v, size %v; want %d", n, err, fi, size)
		}
	}
	if _, err := os.Stat(path + ".4"); !os.IsNotExist(err) {
		t.Error("kept more than 3 old logs")
	}
}

func TestEnsureRunID(t *testing.T) {
	t.Setenv(EnvRunID, "")
	ctx := EnsureRunID(context.Background(), "org-")
	if id := RunID(ctx); !strings.HasPrefix(id, "org-") || len(id) < 10 {
		t.Errorf("new run id %q", id)
	}
	if got := RunID(EnsureRunID(WithRunID(context.Background(), "exec-1"), "org-")); got != "exec-1" {
		t.Errorf("an existing run id was replaced: %q", got)
	}
	t.Setenv(EnvRunID, "parent")
	if got := RunID(EnsureRunID(context.Background(), "org-")); got != "parent" {
		t.Errorf("the inherited run id was replaced: %q", got)
	}
}
