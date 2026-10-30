package health

import (
	"context"
	"fmt"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// CheckCoderSpawnIntents warns when the host's sweep holds a spawn for the operator: the lead of that spawn cannot
// spawn again until a person ends it.
const CheckCoderSpawnIntents = "coder.spawn.intents"

func coderSpawnIntentChecks() []Check {
	return []Check{{ID: CheckCoderSpawnIntents, Group: GroupCore, Title: "Coder spawns held for you",
		DependsOn: []string{CheckDB}, Run: checkCoderSpawnIntents}}
}

func checkCoderSpawnIntents(_ context.Context, env *Env) Result {
	if env.DB == nil {
		return Result{Status: StatusSkip, Summary: "not available"}
	}
	n, err := coderhost.NewStore(env.DB).CountHeldSpawnIntents()
	if err != nil { // a database from before the table existed
		return Result{Status: StatusSkip, Summary: "not available", Detail: err.Error()}
	}
	if n == 0 {
		return Result{Status: StatusOK, Summary: "none"}
	}
	return Result{Status: StatusWarn, Summary: fmt.Sprintf("%d coder spawn(s) held for you", n),
		Detail: "the lead of each cannot spawn again; see them with: monoagentcli --profile P coder sessions spawn-intents list"}
}
