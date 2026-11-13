package data

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
)

// DebugHelperNode helps test workflows: it throws an error on demand or
// generates fake data. Type: "data.debug_helper" (n8n: DebugHelper).
//
// Config:
//
//	category              do_nothing | throw_error | random_data (n8n's "oom" is not supported)
//	throw_error_type      NodeApiError | NodeOperationError | Error (text of the error kind)
//	throw_error_message   error message (default "Node has thrown an error")
//	random_data_type      user | email | address | credit_card | uuid | mac_address | ipv4 | ipv6 |
//	                      lat_long | semver | url | nanoid
//	random_data_count     items per input item (default 10)
//	random_data_seed      seed for reproducible data (empty = random)
//	random_data_single_array  emit one item {generatedItems: [...]} instead of one item each
//	nanoid_alphabet, nanoid_length  for nanoid
//
// do_nothing emits no items, as n8n does.
type DebugHelperNode struct{}

func (n *DebugHelperNode) Type() string { return "data.debug_helper" }

// PublishesToThirdParty: data.debug_helper sends nothing off this machine (issue #1418).
func (DebugHelperNode) PublishesToThirdParty() bool { return false }

// ErrDebugHelper is the error thrown by category throw_error.
var ErrDebugHelper = errors.New("debug helper")

var debugCategoryAliases = map[string]string{"doNothing": "do_nothing", "throwError": "throw_error", "randomData": "random_data"}

var randomTypeAliases = map[string]string{"creditCard": "credit_card", "macAddress": "mac_address", "latLong": "lat_long"}

func (n *DebugHelperNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	category := cfgString(config, "category", "throw_error")
	if a, ok := debugCategoryAliases[category]; ok {
		category = a
	}
	switch category {
	case "do_nothing":
		return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{}}}, nil
	case "throw_error":
		kind := cfgString(config, "throw_error_type", "NodeApiError")
		msg := cfgString(config, "throw_error_message", "Node has thrown an error")
		return nil, fmt.Errorf("data.debug_helper: %w (%s): %s", ErrDebugHelper, kind, msg)
	case "oom", "out_of_memory":
		return nil, fmt.Errorf("data.debug_helper: category %q is not supported: mono-agent does not simulate running out of memory", category)
	case "random_data":
	default:
		return nil, fmt.Errorf("data.debug_helper: category: unknown value %q (want do_nothing, throw_error or random_data)", category)
	}

	kind := cfgString(config, "random_data_type", "user")
	if a, ok := randomTypeAliases[kind]; ok {
		kind = a
	}
	count, err := cfgInt(config, "random_data_count", 10)
	if err != nil || count < 0 || count > 100000 {
		return nil, fmt.Errorf("data.debug_helper: random_data_count: must be between 0 and 100000")
	}
	nanoLen, err := cfgInt(config, "nanoid_length", 16)
	if err != nil || nanoLen < 1 || nanoLen > 1024 {
		return nil, fmt.Errorf("data.debug_helper: nanoid_length: must be between 1 and 1024")
	}
	g := newFakeGen(cfgString(config, "random_data_seed", ""))
	g.alphabet = cfgString(config, "nanoid_alphabet", "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")
	g.nanoLen = nanoLen
	gen, ok := g.generators()[kind]
	if !ok {
		return nil, fmt.Errorf("data.debug_helper: random_data_type: unknown value %q", kind)
	}
	single := cfgBool(config, "random_data_single_array", false)

	inputs := len(input.Items)
	if inputs == 0 {
		inputs = 1
	}
	var out []workflow.Item
	for i := 0; i < inputs; i++ {
		generated := make([]interface{}, count)
		for k := range generated {
			generated[k] = gen()
		}
		if single {
			out = append(out, workflow.Item{JSON: map[string]interface{}{"generatedItems": generated}})
			continue
		}
		for _, v := range generated {
			out = append(out, workflow.Item{JSON: v.(map[string]interface{})})
		}
	}
	return []workflow.NodeOutput{{Handle: "main", Items: out}}, nil
}

type fakeGen struct {
	r        *rand.Rand
	alphabet string
	nanoLen  int
}

func newFakeGen(seed string) *fakeGen {
	var s1, s2 uint64
	if seed == "" {
		s1, s2 = rand.Uint64(), rand.Uint64()
	} else {
		h := fnv.New64a()
		h.Write([]byte(seed))
		s1 = h.Sum64()
		s2 = s1 ^ 0x9e3779b97f4a7c15
	}
	return &fakeGen{r: rand.New(rand.NewPCG(s1, s2))}
}

var (
	fakeFirst   = []string{"Ada", "Alan", "Grace", "Linus", "Margaret", "Dennis", "Barbara", "Ken", "Frances", "Edsger", "Radia", "Tim", "Hedy", "John", "Katherine", "Guido"}
	fakeLast    = []string{"Lovelace", "Turing", "Hopper", "Torvalds", "Hamilton", "Ritchie", "Liskov", "Thompson", "Allen", "Dijkstra", "Perlman", "Berners-Lee", "Lamarr", "McCarthy", "Johnson", "Rossum"}
	fakeStreets = []string{"Main St", "Oak Ave", "Maple Dr", "Cedar Ln", "Elm St", "Pine Rd", "Lake View", "Hill Crest"}
	fakeCities  = []string{"Springfield", "Riverton", "Fairview", "Lakeside", "Greenville", "Bristol", "Clinton", "Madison"}
	fakeStates  = []string{"California", "Texas", "New York", "Oregon", "Ohio", "Colorado", "Georgia", "Maine"}
	fakeCountry = []string{"United States", "Canada", "Germany", "France", "Japan", "Brazil", "Australia", "Netherlands"}
	fakeDomains = []string{"example.com", "example.org", "example.net", "test.dev"}
	fakeTLDs    = []string{"com", "org", "net", "io", "dev"}
)

func (g *fakeGen) pick(list []string) string { return list[g.r.IntN(len(list))] }

func (g *fakeGen) digits(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteByte(byte('0' + g.r.IntN(10)))
	}
	return b.String()
}

func (g *fakeGen) uuid() string {
	var b [16]byte
	for i := range b {
		b[i] = byte(g.r.UintN(256))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (g *fakeGen) email() string {
	return strings.ToLower(g.pick(fakeFirst)+"."+g.pick(fakeLast)) + g.digits(2) + "@" + g.pick(fakeDomains)
}

// luhn returns a 16-digit card number starting with prefix that passes the
// Luhn check.
func (g *fakeGen) luhn(prefix string) string {
	num := prefix + g.digits(15-len(prefix))
	sum := 0
	for i := len(num) - 1; i >= 0; i-- {
		d := int(num[i] - '0')
		if (len(num)-i)%2 == 1 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return num + string(rune('0'+(10-sum%10)%10))
}

func (g *fakeGen) generators() map[string]func() interface{} {
	return map[string]func() interface{}{
		"user": func() interface{} {
			return map[string]interface{}{"uid": g.uuid(), "email": g.email(), "firstname": g.pick(fakeFirst), "lastname": g.pick(fakeLast), "password": g.nanoid("abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789", 12)}
		},
		"email": func() interface{} {
			return map[string]interface{}{"email": g.email(), "confirmed": g.r.IntN(2) == 1}
		},
		"address": func() interface{} {
			return map[string]interface{}{"firstname": g.pick(fakeFirst), "lastname": g.pick(fakeLast), "street": fmt.Sprintf("%d %s", 1+g.r.IntN(9999), g.pick(fakeStreets)), "city": g.pick(fakeCities), "zip": g.digits(5), "state": g.pick(fakeStates), "country": g.pick(fakeCountry)}
		},
		"credit_card": func() interface{} {
			typ, prefix := "Visa", "4"
			if g.r.IntN(2) == 1 {
				typ, prefix = "MasterCard", fmt.Sprintf("5%d", 1+g.r.IntN(5))
			}
			return map[string]interface{}{"type": typ, "number": g.luhn(prefix), "ccv": g.digits(3), "exp": fmt.Sprintf("%02d/%02d", 1+g.r.IntN(12), 1+g.r.IntN(40)), "holder_name": g.pick(fakeFirst) + " " + g.pick(fakeLast)}
		},
		"uuid": func() interface{} { return map[string]interface{}{"uuid": g.uuid()} },
		"nanoid": func() interface{} {
			return map[string]interface{}{"nanoId": g.nanoid(g.alphabet, g.nanoLen)}
		},
		"url": func() interface{} {
			return map[string]interface{}{"url": "https://" + strings.ToLower(g.pick(fakeLast)) + "." + g.pick(fakeTLDs)}
		},
		"ipv4": func() interface{} {
			return map[string]interface{}{"ip": fmt.Sprintf("%d.%d.%d.%d", 1+g.r.IntN(223), g.r.IntN(256), g.r.IntN(256), 1+g.r.IntN(254))}
		},
		"ipv6": func() interface{} {
			parts := make([]string, 8)
			for i := range parts {
				parts[i] = fmt.Sprintf("%x", g.r.IntN(65536))
			}
			return map[string]interface{}{"ipv6": strings.Join(parts, ":")}
		},
		"mac_address": func() interface{} {
			parts := make([]string, 6)
			for i := range parts {
				parts[i] = fmt.Sprintf("%02x", g.r.IntN(256))
			}
			return map[string]interface{}{"mac": strings.Join(parts, ":")}
		},
		"lat_long": func() interface{} {
			return map[string]interface{}{"location": fmt.Sprintf("%.6f, %.6f", g.r.Float64()*180-90, g.r.Float64()*360-180)}
		},
		"semver": func() interface{} {
			return map[string]interface{}{"version": fmt.Sprintf("%d.%d.%d", g.r.IntN(10), g.r.IntN(20), g.r.IntN(50))}
		},
	}
}

func (g *fakeGen) nanoid(alphabet string, n int) string {
	runes := []rune(alphabet)
	if len(runes) == 0 {
		runes = []rune("0123456789abcdefghijklmnopqrstuvwxyz")
	}
	out := make([]rune, n)
	for i := range out {
		out[i] = runes[g.r.IntN(len(runes))]
	}
	return string(out)
}
