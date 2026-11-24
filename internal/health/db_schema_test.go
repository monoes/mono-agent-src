package health

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDBSchemaCheck(t *testing.T) {
	hook := func(dbV, binV int, ahead bool, err error) func(context.Context) (int, int, bool, error) {
		return func(context.Context) (int, int, bool, error) { return dbV, binV, ahead, err }
	}
	cases := []struct {
		name    string
		env     Env
		status  Status
		summary string
	}{
		{"no hook", Env{}, StatusSkip, "not available"},
		{"current", Env{SchemaAhead: hook(65, 65, false, nil)}, StatusOK, "v065"},
		{"ahead", Env{SchemaAhead: hook(99, 65, true, nil)}, StatusWarn, "newer monoagent (v099 > v065)"},
		{"unreadable", Env{SchemaAhead: hook(0, 65, false, errors.New("boom"))}, StatusWarn, "cannot read"},
	}
	for _, c := range cases {
		res := checkDBSchema(context.Background(), &c.env)
		if res.Status != c.status || !strings.Contains(res.Summary, c.summary) {
			t.Errorf("%s: %s %q, want %s containing %q", c.name, res.Status, res.Summary, c.status, c.summary)
		}
		if res.FixID != "" {
			t.Errorf("%s: offers fix %s; the row only warns", c.name, res.FixID)
		}
	}
}

func TestDBBackupsCheck(t *testing.T) {
	size := func(n int, b int64, err error) func() (int, int64, error) {
		return func() (int, int64, error) { return n, b, err }
	}
	cases := []struct {
		name    string
		env     Env
		status  Status
		summary string
	}{
		{"no hook", Env{}, StatusSkip, "not available"},
		{"small", Env{ProfileBackupsSize: size(3, 5<<20, nil)}, StatusOK, "3 snapshot(s), 5.0 MB"},
		{"large", Env{ProfileBackupsSize: size(9, 2<<30, nil)}, StatusWarn, "9 snapshot(s)"},
		{"unreadable", Env{ProfileBackupsSize: size(0, 0, errors.New("boom"))}, StatusWarn, "cannot read"},
	}
	for _, c := range cases {
		res := checkDBBackups(context.Background(), &c.env)
		if res.Status != c.status || !strings.Contains(res.Summary, c.summary) {
			t.Errorf("%s: %s %q, want %s containing %q", c.name, res.Status, res.Summary, c.status, c.summary)
		}
	}
}

func TestDBSchemaRowIsRegisteredAfterTheDatabase(t *testing.T) {
	for _, c := range Default().Checks() {
		if c.ID != CheckDBSchema {
			continue
		}
		if c.Group != GroupCore || c.Required || len(c.DependsOn) != 1 || c.DependsOn[0] != CheckDB {
			t.Fatalf("core.db.schema registered as %+v", c)
		}
		return
	}
	t.Fatal("core.db.schema is not registered")
}
