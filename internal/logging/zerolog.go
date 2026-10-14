package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// Zerolog returns a zerolog.Logger at the root level whose events are
// written through the root slog handler — same format, same destination,
// and the root's run_id — instead of raw JSON on stderr. It replaces
// zerolog.New(os.Stderr).With().Timestamp().Logger().Level(...) in code that
// still takes a zerolog.Logger; add fields with .With() as before. The level
// is read when it is called, so call it after Init.
func Zerolog() zerolog.Logger {
	return zerolog.New(zerologWriter{}).Level(zerologLevel(Level()))
}

// ZerologContext is Zerolog with the run_id WithRunID put in ctx as a
// field (the process's own MONOAGENT_RUN_ID is on the root logger already).
func ZerologContext(ctx context.Context) zerolog.Logger {
	l := Zerolog()
	if id, _ := ctx.Value(runIDKey).(string); id != "" && id != os.Getenv(EnvRunID) {
		l = l.With().Str(KeyRunID, id).Logger()
	}
	return l
}

// demoted maps a Zerolog event message to the level Demote set for it.
var demoted sync.Map // string → slog.Level

// Demote logs the Zerolog events whose message is one of msgs at level
// instead of their own, filtered by the root level like any slog record. A
// one-shot command that reports those events itself (a run's summary names
// the failed node) uses it to keep them off the terminal at the default
// level. The returned func undoes it.
func Demote(level slog.Level, msgs ...string) (undo func()) {
	for _, m := range msgs {
		demoted.Store(m, level)
	}
	return func() {
		for _, m := range msgs {
			demoted.Delete(m)
		}
	}
}

// zerologLevel maps a slog level onto zerolog's.
func zerologLevel(l slog.Level) zerolog.Level {
	switch {
	case l < slog.LevelDebug:
		return zerolog.TraceLevel
	case l < slog.LevelInfo:
		return zerolog.DebugLevel
	case l < slog.LevelWarn:
		return zerolog.InfoLevel
	case l < slog.LevelError:
		return zerolog.WarnLevel
	}
	return zerolog.ErrorLevel
}

// slogLevel maps a zerolog level onto slog's.
func slogLevel(l zerolog.Level) slog.Level {
	switch l {
	case zerolog.TraceLevel:
		return slog.LevelDebug - 4
	case zerolog.DebugLevel:
		return slog.LevelDebug
	case zerolog.InfoLevel, zerolog.NoLevel:
		return slog.LevelInfo
	case zerolog.WarnLevel:
		return slog.LevelWarn
	}
	return slog.LevelError
}

// zerologWriter turns each zerolog event (one JSON object) into a record of
// the default slog logger.
type zerologWriter struct{}

func (w zerologWriter) Write(p []byte) (int, error) {
	return w.WriteLevel(zerolog.NoLevel, p)
}

func (zerologWriter) WriteLevel(zl zerolog.Level, p []byte) (int, error) {
	// The zerolog logger's own level already chose this event (a caller may
	// lower it for --verbose), so the root level is not applied again.
	level := slogLevel(zl)
	l := slog.Default()
	ctx := context.Background()
	var fields map[string]any
	dec := json.NewDecoder(bytes.NewReader(p))
	dec.UseNumber()
	if err := dec.Decode(&fields); err != nil {
		// Not an event zerolog wrote; pass the text on as it is.
		r := slog.NewRecord(time.Now(), level, string(bytes.TrimSpace(p)), 0)
		return len(p), l.Handler().Handle(ctx, r)
	}
	msg, _ := fields[zerolog.MessageFieldName].(string)
	if v, ok := demoted.Load(msg); ok {
		level = v.(slog.Level)
		if !l.Enabled(ctx, level) {
			return len(p), nil
		}
	}
	delete(fields, zerolog.MessageFieldName)
	delete(fields, zerolog.LevelFieldName)
	delete(fields, zerolog.TimestampFieldName)
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	r := slog.NewRecord(time.Now(), level, msg, 0)
	for _, k := range keys {
		r.AddAttrs(slog.Any(k, fields[k]))
	}
	return len(p), l.Handler().Handle(ctx, r)
}
