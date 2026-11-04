package monomind

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func envHas(env []string, name string) bool {
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); k == name {
			return true
		}
	}
	return false
}

func asOperator(t *testing.T, operator bool) {
	t.Helper()
	SetOperatorCheck(func() bool { return operator })
	t.Cleanup(func() { SetOperatorCheck(nil) })
}

func TestFilteredEnvironDestinations(t *testing.T) {
	destinations := append([]string{
		"FOO_PROVIDER_BASE_URL", "NEWLLM_API_BASE", "NEWLLM_API_HOST", "MISTRAL_SERVER_URL", "GROQ_ENDPOINT",
		"OLLAMA_MODELS", "OLLAMA_ORIGINS", "http_proxy", "https_proxy", "no_proxy", "MY_PROXY", "AWS_ENDPOINT_URL_S3",
		"GOOGLE_GENAI_BASE", "AZURE_FOO_ENDPOINT_V2", "MY_CA_BUNDLE", "SSL_CERT_DIR", "DEEPSEEK_URL",
	}, destinationEnvVars...)
	keep := []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GOOGLE_API_KEY", "AWS_SECRET_ACCESS_KEY", "OPENROUTER_API_KEY",
		"HOME_URL_UNRELATED", "UNRELATED_VAR", "MONOAGENT_OTHER", "AWS_REGION",
	}
	setAll := func(t *testing.T) {
		for _, k := range append(append([]string{}, destinations...), keep...) {
			t.Setenv(k, "x")
		}
	}
	t.Run("operator keeps all", func(t *testing.T) {
		setAll(t)
		asOperator(t, true)
		env := FilteredEnviron()
		for _, k := range append(append([]string{}, destinations...), keep...) {
			if !envHas(env, k) {
				t.Errorf("operator lost %s", k)
			}
		}
	})
	t.Run("non-operator loses destinations only", func(t *testing.T) {
		setAll(t)
		asOperator(t, false)
		env := FilteredEnviron()
		for _, k := range destinations {
			if envHas(env, k) {
				t.Errorf("non-operator kept destination %s", k)
			}
		}
		for _, k := range keep {
			if !envHas(env, k) {
				t.Errorf("non-operator lost %s", k)
			}
		}
	})
	t.Run("unregistered falls back to the agent marker", func(t *testing.T) {
		setAll(t)
		SetOperatorCheck(nil)
		if !envHas(FilteredEnviron(), "ANTHROPIC_BASE_URL") {
			t.Error("unmarked unregistered process lost ANTHROPIC_BASE_URL")
		}
		t.Setenv("OPENCODE", "1")
		if envHas(FilteredEnviron(), "ANTHROPIC_BASE_URL") {
			t.Error("marked unregistered process kept ANTHROPIC_BASE_URL")
		}
	})
}

// The real exec path: a stub runner reports the destination variable it was started with.
func TestExecDestinationsReachStubRunnerOnlyForOperator(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub runner is a shell script")
	}
	stub := filepath.Join(t.TempDir(), "monomind")
	script := "#!/bin/sh\nprintf '%s|%s' \"$ANTHROPIC_BASE_URL\" \"$ANTHROPIC_API_KEY\" > \"$STUB_OUT\"\n" +
		`echo '{"v":1,"type":"done","exit_code":0}'` + "\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(operator bool) string {
		out := filepath.Join(t.TempDir(), "seen")
		t.Setenv("STUB_OUT", out)
		t.Setenv("ANTHROPIC_BASE_URL", "http://attacker.invalid")
		t.Setenv("ANTHROPIC_API_KEY", "key")
		asOperator(t, operator)
		if _, err := Exec(context.Background(), ExecOptions{Bin: stub, Runtime: "claude", Prompt: "hi"}, func(Event) {}); err != nil {
			t.Fatalf("exec: %v", err)
		}
		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := run(true); got != "http://attacker.invalid|key" {
		t.Errorf("operator: runner saw %q", got)
	}
	if got := run(false); got != "|key" {
		t.Errorf("marked agent: runner saw %q, want no base URL and the key still present", got)
	}
}

// Every os/exec use in the packages that start the monomind runner builds its environment through
// FilteredEnviron or ScrubDestinations, or is listed here with the reason it does not run the runner.
func TestSourcePinEveryRunnerExecIsScrubbed(t *testing.T) {
	exempt := map[string]string{
		"internal/monomind/pin_path.go":                   "runs a package manager, scrubManagerEnv",
		"internal/monomind/pin_shims.go":                  "runs the node version manager's `which`",
		"internal/dynorg/worktree_git.go":                 "runs git",
		"internal/cli/chat/coder_background.go":           "runs ps",
		"internal/cli/chat/coder_host_signal_windows.go":  "runs taskkill",
		"internal/cli/chat/coder_host_stamp_other.go":     "runs ps",
		"internal/cli/chat/coder_host.go":                 "starts monoagentcli itself (a process with its own markers)",
		"internal/cli/chat/coder_host_exec.go":            "starts monoagentcli itself (a process with its own markers)",
		"internal/cli/chat/coder_queue_add.go":            "starts monoagentcli itself",
		"internal/nodes/system/execute_command.go":        "a user-configured command, not the runner",
		"internal/nodes/system/execute_command_schema.go": "a user-configured command, not the runner",
		"internal/nodes/control/code_python.go":           "runs python",
		"internal/nodes/mcp/stdio.go":                     "a user-configured MCP server",
		"cmd/monoagentcli/hil.go":                         "starts monoagentcli itself",
		"cmd/monoagentcli/library_adapter.go":             "opens a URL",
		"cmd/monoagentcli/update.go":                      "runs the updater",
	}
	root := filepath.Join("..", "..")
	var scanned int
	for _, dir := range []string{"internal/monomind", "internal/dynorg", "internal/cli/chat", "internal/ai", "internal/nodes", "cmd/monoagentcli"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			file, perr := parser.ParseFile(token.NewFileSet(), p, nil, 0)
			if perr != nil {
				return perr
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				var execs, scrub bool
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					switch f := call.Fun.(type) {
					case *ast.SelectorExpr:
						if x, ok := f.X.(*ast.Ident); ok && x.Name == "exec" && (f.Sel.Name == "Command" || f.Sel.Name == "CommandContext") {
							execs = true
						}
						if f.Sel.Name == "FilteredEnviron" || f.Sel.Name == "ScrubDestinations" || f.Sel.Name == "CommandContext" && isMonomindPkg(f.X) || f.Sel.Name == "Command" && isMonomindPkg(f.X) {
							scrub = true
						}
					case *ast.Ident:
						if f.Name == "FilteredEnviron" || f.Name == "ScrubDestinations" {
							scrub = true
						}
					}
					return true
				})
				if !execs {
					continue
				}
				scanned++
				if scrub || exempt[rel] != "" || exempt[rel+":"+fn.Name.Name] != "" {
					continue
				}
				t.Errorf("%s: %s starts a process without FilteredEnviron/ScrubDestinations; route it through the filter or list it as exempt with the reason", rel, fn.Name.Name)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if scanned < 10 {
		t.Fatalf("the pin saw only %d exec uses; it is not scanning", scanned)
	}
}

func isMonomindPkg(x ast.Expr) bool {
	id, ok := x.(*ast.Ident)
	return ok && id.Name == "monomind"
}
