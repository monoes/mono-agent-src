package verifier

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
	"github.com/monoes/mono-agent/internal/monomind"
)

// Placeholders: the red state of the guard tests. The next commit replaces this file.

const platformOK = true
const absurdFactor = 100

var ErrBusy = errors.New("busy")

type ExecFunc func(ctx context.Context, opts monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error)

type Meter interface {
	Reserve(ctx context.Context, capUSD float64) (settle func(chargedMicro int64, known bool), err error)
}

func PinKey(p string) string                                     { return p }
func Independent(j, c, l string) (bool, string)                  { return true, "" }
func Excerpt(s string, max int) string                           { return s }
func defang(s string) string                                     { return s }
func reasonCode(s string) string                                 { return s }
func ChargeFor(usd float64, known bool, c float64) (int64, bool) { return 0, true }

type costing struct{ inner ExecFunc }

func (c *costing) exec(ctx context.Context, o monomind.ExecOptions, on func(monomind.Event)) (*monomind.TurnResult, error) {
	return c.inner(ctx, o, on)
}
func (c *costing) charge(float64) (int64, bool) { return 0, true }

type GroupPlace struct{}

func (*GroupPlace) Place(*exec.Cmd) (func(), error) { return func() {}, nil }
func (*GroupPlace) Close()                          {}

type FakeStep struct {
	Result         Result
	Err            error
	UntilCancelled bool
}
type Fake struct{ pin string }

func NewFake(pin string, steps ...FakeStep) *Fake { return &Fake{pin: pin} }
func (f *Fake) Pin() string                       { return f.pin }
func (f *Fake) Calls() []Request                  { return nil }
func (f *Fake) Run(context.Context, Request) (Result, error) {
	return Result{Outcome: OutcomePassedMachine}, nil
}

type Config struct {
	Checker, ScratchBase           string
	Meter                          Meter
	Exec                           ExecFunc
	Workspace                      func() (string, error)
	CallCapUSD                     float64
	ReviewTimeout                  time.Duration
	CriterionTimeout, TotalTimeout time.Duration
	NetDenyAvailable               func() bool
	afterCriteria                  func()
}

type Checker struct {
	cfg       Config
	capUSD    float64
	crit, tot time.Duration
	run       sync.Mutex
}

func New(c Config) (*Checker, error) { return &Checker{cfg: c}, nil }
func (k *Checker) Pin() string       { return k.cfg.Checker }
func (k *Checker) Run(context.Context, Request) (Result, error) {
	return Result{Outcome: OutcomePassedMachine}, nil
}
func (k *Checker) conclude(context.Context, Request, finish.Tree, *Result) (Result, error) {
	return Result{Outcome: OutcomePassedMachine}, nil
}
