package mcp

// The other tests of these tools run as the default profile, whose id is also what a
// handler that forgot the server's profile and wrote "default" would use: they
// cannot tell a scoped tool from an unscoped one. These run as a profile whose id is
// neither "default" nor its own name, as the id of a profile made by `profile create`
// or the desktop app is, and the server is opened by that name, so that finding the
// id is part of what is tested.

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/apikeys"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

const (
	workProfileID   = "p-7f3a9c"
	workProfileName = "Work"
)

// workServer is a server, with mutations allowed, of the profile workProfileName,
// and a second handle on its database.
func workServer(t *testing.T) (*Server, *storage.Database) {
	t.Helper()
	dbPath := testdb.Path(t)
	side := sideDB(t, dbPath)
	if _, err := side.DB.Exec(`INSERT INTO profiles (id, name) VALUES (?, ?)`, workProfileID, workProfileName); err != nil {
		t.Fatal(err)
	}
	return serverOver(t, dbPath, workProfileName, true), side
}

func TestAPIKeyToolsBelongToTheServersProfileByItsID(t *testing.T) {
	s, side := workServer(t)
	ctx := context.Background()
	store := apikeys.NewStore(side.DB)
	if _, _, err := store.Create(ctx, "default", "theirs", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Create(ctx, workProfileID, "mine", true); err != nil {
		t.Fatal(err)
	}

	// api_key_list lists the keys of the profile by its id: not the name it was opened
	// by, and not "default".
	if got := namesOf(decodeKeys(t, mustCall(t, s, "api_key_list", map[string]any{"include_revoked": true}))); !reflect.DeepEqual(got, []string{"mine"}) {
		t.Errorf("api_key_list = %v, want only this profile's [mine]", got)
	}
	// The tools that change none name the profile by its id in what the user runs.
	for tool, args := range map[string]map[string]any{
		"api_key_create": {"name": "made"},
		"api_key_update": {"id": "mine", "context": false},
		"api_key_revoke": {"id": "mine"},
	} {
		command := strings.TrimPrefix(tool, "api_key_")
		if _, err := callAPITool(t, s, tool, args); err == nil || !strings.Contains(err.Error(), "--profile "+workProfileID+" api key "+command) {
			t.Errorf("%s on the server of profile %s: %v, want a refusal that names it by its id", tool, workProfileID, err)
		}
	}
}

// Whether auto works is read from the Jev settings of the profile by its id: the
// surface switched on for the default profile does nothing for the work profile.
func TestAPIModelsListReadsAutoOfTheServersProfileByItsID(t *testing.T) {
	pinAPIEnv(t)
	fakeAPIMonomind(t)
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	s, side := workServer(t)

	if err := jevconf.SetEnabled(side.DB, "default", jevconf.APIAuto, true); err != nil {
		t.Fatal(err)
	}
	if r := modelsReport(t, s, nil); r.Auto.Available || !strings.Contains(r.Auto.Missing, "jev enable api_auto") {
		t.Errorf("the surface is on for the default profile only: %+v", r.Auto)
	}

	if err := jevconf.SetEnabled(side.DB, workProfileID, jevconf.APIAuto, true); err != nil {
		t.Fatal(err)
	}
	if r := modelsReport(t, s, nil); !r.Auto.Available || r.Auto.KeySource != "env" {
		t.Errorf("the surface is on for the work profile, and a key is in the environment: %+v", r.Auto)
	}
}
