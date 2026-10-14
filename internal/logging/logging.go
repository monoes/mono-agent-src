// Package logging is monoagent's one root logger: log/slog, a text handler on
// stderr by default (JSON with MONOAGENT_LOG_FORMAT=json), at the level given
// by --log-level or MONOAGENT_LOG_LEVEL (debug|info|warn|error, default warn).
//
// Init also routes the standard library's log package into it, so the
// existing log.Printf calls get a level: a line is INFO unless it is marked
// WARN/WARNING/ERROR (see stdLevel). At the default level those INFO lines —
// "applied migration NNN", storage reconcile notes — stay out of the output
// of every command; --log-level=info shows them again.
//
// Zerolog code adopts the same level and output through Zerolog
// (zerolog.go), and a run's id travels through the context and the
// MONOAGENT_RUN_ID environment variable (context.go).
package logging

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// Environment variables read by Init. EnvRunID is in context.go.
const (
	EnvLevel  = "MONOAGENT_LOG_LEVEL"
	EnvFormat = "MONOAGENT_LOG_FORMAT"
)

// DefaultLevel keeps the output commands had before this package: warnings
// and errors only.
const DefaultLevel = slog.LevelWarn

// levelVar is the level of the root logger and of every Zerolog logger. It
// starts at DefaultLevel even when Init never runs (the Wails app, tests).
var levelVar = func() *slog.LevelVar {
	v := new(slog.LevelVar)
	v.Set(DefaultLevel)
	return v
}()

// levelChosen records whether Init found a level given by --log-level or
// EnvLevel, rather than DefaultLevel.
var levelChosen atomic.Bool

// LevelChosen reports whether the user chose the root level (--log-level or
// MONOAGENT_LOG_LEVEL) instead of leaving the default.
func LevelChosen() bool { return levelChosen.Load() }

// Level is the current root level.
func Level() slog.Level { return levelVar.Level() }

// SetLevel changes the root level.
func SetLevel(l slog.Level) { levelVar.Set(l) }

// ParseLevel reads debug|info|warn|warning|error (any case). An empty string
// is DefaultLevel.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return DefaultLevel, nil
	case "debug", "trace":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return DefaultLevel, fmt.Errorf("unknown log level %q (want debug, info, warn or error)", s)
}

// LevelFlag returns the value of --log-level in args (`--log-level X` or
// `--log-level=X`), and whether it was given. Init reads it before cobra
// parses the command line, so that what runs before a command's own code
// (migrations on opening the database) is already filtered.
func LevelFlag(args []string) (string, bool) { return flagValue(args, "--log-level") }

// FileFlag returns the value of --log-file in args, and whether it was given.
func FileFlag(args []string) (string, bool) { return flagValue(args, "--log-file") }

// flagValue returns the value of flag in args (`flag X` or `flag=X`).
func flagValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == "--" {
			break
		}
		if v, ok := strings.CutPrefix(a, flag+"="); ok {
			return v, true
		}
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// Init installs the root logger writing to w — or to the file args'
// --log-file names (appended to, created 0600; when it cannot be opened the
// logger stays on w and says so): the level from args' --log-level, else
// getenv(EnvLevel), else DefaultLevel; the format from
// getenv(EnvFormat) ("json", else text). An unknown level is reported as a
// warning and the default is kept. A level given by flag is also exported as
// EnvLevel, so child processes (a daemon started by doctor, CLI subprocesses)
// log at the same level. MONOAGENT_RUN_ID, when set, is attached to every
// line as run_id.
func Init(args []string, getenv func(string) string, w io.Writer) {
	raw, fromFlag := LevelFlag(args)
	if !fromFlag {
		raw = getenv(EnvLevel)
	}
	level, levelErr := ParseLevel(raw)
	levelVar.Set(level)
	levelChosen.Store(levelErr == nil && strings.TrimSpace(raw) != "")
	if fromFlag && levelErr == nil {
		_ = os.Setenv(EnvLevel, raw)
	}

	var fileErr error
	if path, ok := FileFlag(args); ok && strings.TrimSpace(path) != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			fileErr = fmt.Errorf("--log-file: %w — logging to stderr", err)
		} else {
			w = f // open for the life of the process
		}
	}

	opts := &slog.HandlerOptions{Level: levelVar}
	var h slog.Handler
	if strings.EqualFold(strings.TrimSpace(getenv(EnvFormat)), "json") {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	l := slog.New(h)
	if id := getenv(EnvRunID); id != "" {
		l = l.With(KeyRunID, id)
	}
	slog.SetDefault(l)
	// slog.SetDefault already sends the log package to the handler, but at
	// one fixed level; stdWriter gives each line the level it is marked with.
	log.SetFlags(0)
	log.SetOutput(stdWriter{})

	if levelErr != nil {
		slog.Warn(levelErr.Error())
	}
	if fileErr != nil {
		slog.Warn(fileErr.Error())
	}
}

// stdWriter is the log package's output after Init: each line becomes a
// record of the default logger, at stdLevel's level.
type stdWriter struct{}

func (stdWriter) Write(p []byte) (int, error) {
	msg := strings.TrimRight(string(p), "\n")
	level, msg := stdLevel(msg)
	l := slog.Default()
	ctx := context.Background()
	if !l.Enabled(ctx, level) {
		return len(p), nil
	}
	r := slog.NewRecord(time.Now(), level, msg, 0)
	if err := l.Handler().Handle(ctx, r); err != nil {
		return 0, err
	}
	return len(p), nil
}

// stdLevel is the level of a standard-library log line: WARN or ERROR when
// one of its first "a: b: " segments starts with a WARN/WARNING/ERROR marker
// ("WARN: x", "WARNING http.ssh", "storage: reconcile: WARN: y"), INFO
// otherwise. A leading marker is dropped from the message (the level says
// it); one after a component prefix stays where it is.
func stdLevel(msg string) (slog.Level, string) {
	rest := msg
	for i := 0; i < 4 && rest != ""; i++ {
		rest = strings.TrimLeft(rest, " ")
		tok := rest
		if j := strings.IndexAny(rest, ": "); j >= 0 {
			tok = rest[:j]
		}
		if level, ok := stdMarkers[tok]; ok {
			if i == 0 {
				return level, strings.TrimLeft(strings.TrimPrefix(rest[len(tok):], ":"), " ")
			}
			return level, msg
		}
		_, after, found := strings.Cut(rest, ": ")
		if !found {
			break
		}
		rest = after
	}
	return slog.LevelInfo, msg
}

var stdMarkers = map[string]slog.Level{
	"WARN":    slog.LevelWarn,
	"WARNING": slog.LevelWarn,
	"ERROR":   slog.LevelError,
}
