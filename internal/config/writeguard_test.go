package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

// A raw storage write of a reserved row is refused unless the caller is the
// operator; any other row, and the operator, pass. The refused write leaves
// the row as it was.
func TestStorageSaveConfigGuardsReservedRows(t *testing.T) {
	var marker string
	old := storage.ConfigWriteGuard
	storage.ConfigWriteGuard = newConfigWriteGuard(func() string { return marker })
	t.Cleanup(func() { storage.ConfigWriteGuard = old })

	db := testdb.Open(t)
	if err := db.SaveConfig("memory.runs", "on"); err != nil { // the operator
		t.Fatalf("operator write: %v", err)
	}
	marker = "an agent marker"
	for _, name := range []string{"memory.runs", "workflow.error_workflow", " Coder_Mode", "coder-mission-confirm/x/y"} {
		if err := db.SaveConfig(name, "off"); err == nil || !strings.Contains(err.Error(), "reserved setting") {
			t.Errorf("%q by a non-operator: %v", name, err)
		}
	}
	if e, _ := db.GetConfig("memory.runs"); e == nil || e.ConfigData != "on" {
		t.Errorf("memory.runs after refused writes: %+v", e)
	}
	if e, _ := db.GetConfig("coder_mode"); e != nil {
		t.Errorf("coder_mode was written: %+v", e)
	}
	if err := db.SaveConfig("linkedin_login_x", "{}"); err != nil {
		t.Errorf("an ordinary row: %v", err)
	}
}

// The package installs the guard itself, so every binary that links the
// config store has it; it uses the real operator check.
func TestStorageGuardIsInstalledAtStartup(t *testing.T) {
	if storage.ConfigWriteGuard == nil {
		t.Fatal("storage.ConfigWriteGuard is not installed")
	}
	t.Setenv("MONOAGENT_WORKFLOW_RUN", "wf-1")
	db := testdb.Open(t)
	if err := db.SaveConfig("memory.runs", "off"); err == nil {
		t.Error("a workflow run's raw write of a reserved row passed")
	}
}

// writeAllowlist names every non-test file that touches the configs table in
// SQL (or through Database.SaveConfig/DeleteConfig), with why it may and the
// most statements it may hold. A new writer has to be added here on purpose: it
// must either go through the operator check or only ever write rows no
// agent-reachable command can name. The count means a new raw statement in an
// allowlisted file fails too: raise it only after the same review.
var writeAllowlist = map[string]struct {
	why string
	max int
}{
	"internal/storage/repository_config.go":        {"Database.SaveConfig itself: consults ConfigWriteGuard", 4},
	"internal/config/dbstore.go":                   {"DBConfigStore: refuses every reserved name before it saves", 2},
	"internal/config/manager.go":                   {"generated selector configs: refuses a reserved name, then saves through the store", 2},
	"internal/cli/configcli/config.go":             {"config set/delete: guardReserved asks the operator check first", 4},
	"cmd/monoagentcli/sandbox.go":                  {"sandbox fallback --set: operator-only command", 2},
	"internal/browserctl/permissions.go":           {"browser permissions/site: operator-only commands", 5},
	"internal/cli/chat/coder.go":                   {"coder enable/set: operator-only flags", 2},
	"internal/cli/chat/coder_projects.go":          {"coder projects map/unmap: operator-only", 2},
	"internal/coderhost/missionconfirm.go":         {"the coder host's signed per-send confirmations, written for the operator's own confirm", 5},
	"internal/ai/chat_events_conversations.go":     {"revokes (deletes) confirmations when a conversation is deleted; never writes a value", 1},
	"internal/applypark/cap.go":                    {"reads applications.max_parked only", 1},
	"internal/automation/health_repair.go":         {"reads automation.autoRepair only", 1},
	"internal/browserctl/policy.go":                {"reads the browser allowlist only", 1},
	"internal/cli/statuscli/status.go":             {"counts the rows only", 1},
	"internal/hilsend/gate.go":                     {"reads the send settings row only", 1},
	"internal/monomind/runmemory.go":               {"reads memory.runs only", 1},
	"internal/workflow/error_workflow_settings.go": {"reads workflow.error_workflow only", 1},
}

var (
	sqlLiteral   = regexp.MustCompile("(?s)`[^`]*`|\"(?:\\\\.|[^\"\\\\\\n])*\"")
	sqlKeyword   = regexp.MustCompile(`(?i)\b(select|from|join|into|update|delete|insert|replace)\b`)
	writeKeyword = regexp.MustCompile(`(?i)\b(into|update|delete|insert|replace)\b`)
	configsWord  = regexp.MustCompile("(?i)\\b(select|from|join|into|update|delete|insert|replace)\\s+(?:[\"'`\\[]?\\w+[\"'`\\]]?\\.)?[\"'`\\[]?configs\\b")
	configsCall  = regexp.MustCompile(`(?i)\b(saveconfig|deleteconfig)\(`)
)

// configsStatements counts the places src reaches the configs table: calls of
// SaveConfig/DeleteConfig, every mention of the bare word configs in a string
// that reads as SQL (any quoting or schema; INSERT, REPLACE, UPDATE, DELETE,
// SELECT), and a table name handed in on its own ("configs" as a lone string
// next to a write keyword, as with a formatted statement).
func configsStatements(src string) int {
	// some sources split a keyword across a concatenation
	src = strings.NewReplacer("`+`", "", "\"+\"", "").Replace(src)
	var kept []string
	for _, line := range strings.Split(src, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "//") {
			kept = append(kept, line)
		}
	}
	src = strings.Join(kept, "\n")
	n := len(configsCall.FindAllString(src, -1))
	for _, loc := range sqlLiteral.FindAllStringIndex(src, -1) {
		lit := strings.ReplaceAll(src[loc[0]+1:loc[1]-1], `\`, "")
		switch {
		case sqlKeyword.MatchString(lit):
			n += len(configsWord.FindAllString(lit, -1))
		case strings.TrimSpace(lit) == "configs":
			from := loc[0] - 300
			if from < 0 {
				from = 0
			}
			if writeKeyword.MatchString(src[from:loc[0]]) {
				n++
			}
		}
	}
	return n
}

func TestConfigsStatementsScanner(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		want      int
	}{
		{"insert", "db.Exec(`INSERT INTO configs (name) VALUES (?)`)", 1},
		{"replace without insert", `db.Exec("REPLACE INTO configs VALUES (?)")`, 1},
		{"quoted table", `db.Exec("INSERT INTO \"configs\" VALUES (?)")`, 1},
		{"schema qualified", `db.Exec("UPDATE main.configs SET x = 1")`, 1},
		{"or replace", `db.Exec("INSERT OR REPLACE INTO configs VALUES (?)")`, 1},
		{"read", `db.Query("SELECT config_data FROM configs WHERE name = ?")`, 1},
		{"formatted table name", `db.Exec(fmt.Sprintf("INSERT INTO %s VALUES (?)", "configs"))`, 1},
		{"two statements", "a(`remove FROM configs`); b(`UPDATE configs SET x = 1`)", 2},
		{"split across a concatenation", "db.Exec(`INSERT INTO conf` + `igs VALUES (?)`)", 0},
		{"save call", `db.SaveConfig("x", "y")`, 1},
		{"unrelated word", `log.Info("loaded configs from disk")`, 0},
		{"comment", "// update configs when the user asks\nx := 1", 0},
		{"other table", `db.Exec("INSERT INTO workflow_configs VALUES (?)")`, 0},
	} {
		if got := configsStatements(tc.src); got != tc.want {
			t.Errorf("%s: %d statements, want %d", tc.name, got, tc.want)
		}
	}
}

// No file outside writeAllowlist touches the configs table, and none holds
// more statements than its entry allows.
func TestConfigsTableWritersAreAllowlisted(t *testing.T) {
	root := filepath.Join("..", "..")
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "frontend", "chrome-extension":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		n := configsStatements(string(b))
		if n == 0 {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		seen[rel] = true
		entry, ok := writeAllowlist[rel]
		switch {
		case !ok:
			t.Errorf("%s touches the configs table (%d statements) and is not in writeAllowlist: route it through the operator check or justify it there", rel, n)
		case n > entry.max:
			t.Errorf("%s holds %d configs statements, writeAllowlist allows %d: a new raw statement needs the same review", rel, n, entry.max)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for rel := range writeAllowlist {
		if !seen[rel] {
			t.Errorf("writeAllowlist names %s, which no longer touches the configs table: drop it", rel)
		}
	}
}
