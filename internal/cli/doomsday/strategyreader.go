package doomsday

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/sstore"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// StrategyRunReader is the slice of the store the reader uses to pick a generation and name its state.
type StrategyRunReader interface {
	OpenRuns(ctx context.Context, profile string) ([]store.Run, error)
	NextGeneration(ctx context.Context, profile string) (int64, error)
	GetRun(ctx context.Context, profile string, generation int64) (store.Run, error)
}

// strategyReader is StrategyReader over the MAC-verifying strategy store. It has no write path: it holds the store for
// its read methods only, and the verbs hold it through the StrategyReader interface.
type strategyReader struct {
	st      *sstore.Store
	runs    StrategyRunReader
	profile string
	ready   *readyCalc        // nil: the reading is not shown (a reader built without the run source)
	src     StrategyRunSource // nil: counted criteria are not shown (the charter's criterion texts are unknown)
}

// withRuns lets the status show progress toward counted criteria; it reads the open run's criterion texts through src.
func (r strategyReader) withRuns(src StrategyRunSource) strategyReader {
	r.src = src
	return r
}

// NewStrategyReader builds the reader the verbs use. The store it reads through verifies every row's MAC; a row that
// fails is reported as tampered, with no content, and is never hidden.
func NewStrategyReader(st *sstore.Store, runs StrategyRunReader, profile string) StrategyReader {
	return strategyReader{st: st, runs: runs, profile: profile}
}

// withReady shows the finish-ready reading in the status. The reading is computed, never stored or taken from the table.
func (r strategyReader) withReady(c readyCalc) strategyReader {
	r.ready = &c
	return r
}

// generation resolves 0 to the open run, else the newest generation. A profile with no generation at all has none (0).
func (r strategyReader) generation(ctx context.Context, gen int64) (int64, error) {
	if gen > 0 {
		return gen, nil
	}
	open, err := r.runs.OpenRuns(ctx, r.profile)
	if err != nil {
		return 0, err
	}
	if len(open) > 0 {
		return open[len(open)-1].Generation, nil
	}
	next, err := r.runs.NextGeneration(ctx, r.profile)
	if err != nil || next < 2 {
		return 0, err
	}
	return next - 1, nil
}

func tampered(err error) bool { return errors.Is(err, sstore.ErrTampered) }

func (r strategyReader) ReadStatus(ctx context.Context, gen int64) (StrategyStatusView, error) {
	g, err := r.generation(ctx, gen)
	if err != nil {
		return StrategyStatusView{}, err
	}
	v := StrategyStatusView{Generation: g}
	if g == 0 {
		v.Note = "no run has been recorded for this profile"
		return v, nil
	}
	if run, rerr := r.runs.GetRun(ctx, r.profile, g); rerr == nil {
		v.RunState = string(run.State)
	}
	eps, err := r.st.ListEpochs(ctx, r.profile, g)
	if err == nil {
		var dirs []types.Direction
		if dirs, err = r.st.ListDirections(ctx, r.profile, g); err == nil {
			v.Directions = map[string]int{}
			for _, d := range dirs {
				v.Directions[string(d.State)]++
			}
		}
	}
	if err != nil {
		return markTampered(v, err)
	}
	v.Epochs = len(eps)
	for _, e := range eps {
		if e.Trigger == types.TriggerFinishParked {
			v.ParkedFinish++
		}
	}
	if n := len(eps); n > 0 {
		ev := r.withReason(epochView(eps[n-1]), r.badReasons(ctx, g, eps[n-1:]), g)
		v.LatestEpoch = &ev
	}
	if v.Criteria, err = r.criteriaCounts(ctx, g, eps); err != nil {
		return markTampered(v, err)
	}
	if r.src != nil {
		if run, ok, serr := r.src.Current(ctx); serr == nil && ok && run.Generation == g {
			j, jerr := r.latestJudgment(ctx, g, eps)
			if jerr != nil {
				return markTampered(v, jerr)
			}
			v.Progress = progressViews(run.Criteria, j)
		}
	}
	if r.ready != nil {
		ready, rerr := r.ready.view(ctx, g)
		if rerr != nil {
			return markTampered(v, rerr)
		}
		v.FinishReady = &ready
	}
	return v, nil
}

func markTampered(v StrategyStatusView, err error) (StrategyStatusView, error) {
	if tampered(err) {
		return StrategyStatusView{Generation: v.Generation, RunState: v.RunState, Tampered: true, Note: StrategyTamperedNote}, nil
	}
	return v, err
}

// criteriaCounts counts the judge's readings in the newest judgment, by status. The notes are never read out.
func (r strategyReader) criteriaCounts(ctx context.Context, g int64, eps []types.Epoch) (map[string]int, error) {
	j, err := r.latestJudgment(ctx, g, eps)
	if err != nil || j == nil {
		return nil, err
	}
	out := map[string]int{}
	for _, c := range j.Criteria {
		out[string(c.Status)]++
	}
	return out, nil
}

// latestJudgment is the newest judged epoch's judgment, nil when there is none or its body does not decode.
func (r strategyReader) latestJudgment(ctx context.Context, g int64, eps []types.Epoch) (*types.Judgment, error) {
	for i := len(eps) - 1; i >= 0; i-- {
		e := eps[i]
		if !e.HasJudgment || (e.State != types.EpochJudged && e.State != types.EpochApplied) {
			continue
		}
		rec, err := r.st.GetJudgment(ctx, r.profile, g, e.Seq)
		if err != nil {
			return nil, err
		}
		j, derr := types.DecodeJudgment(rec.Body)
		if derr != nil {
			return nil, nil
		}
		return &j, nil
	}
	return nil, nil
}

// badReasons reads the why of each bad-judgment epoch from the verified audit chain. A run source that cannot say, or a
// chain that does not verify, shows no reason: the epoch's own state is still shown, and never hidden.
func (r strategyReader) badReasons(ctx context.Context, g int64, eps []types.Epoch) map[string]string {
	hasBad := false
	for _, e := range eps {
		hasBad = hasBad || e.State == types.EpochBadJudgment
	}
	src, ok := r.runs.(interface {
		StrategyBadReasons(ctx context.Context, profile string, generation int64) (map[string]string, error)
	})
	if !hasBad || !ok {
		return nil
	}
	m, err := src.StrategyBadReasons(ctx, r.profile, g)
	if err != nil {
		return nil
	}
	return m
}

func (r strategyReader) withReason(v EpochView, reasons map[string]string, g int64) EpochView {
	if v.State == string(types.EpochBadJudgment) {
		v.BadReason = RedactText(reasons[types.EpochID(r.profile, g, v.Seq)], types.MaxBadReasonLen)
	}
	return v
}

func epochView(e types.Epoch) EpochView {
	v := EpochView{Seq: e.Seq, Trigger: string(e.Trigger), State: string(e.State), StartedAt: StampTime(e.StartedAt), ModelPin: RedactText(e.ModelPin, strategyPinCap)}
	if e.HasJudgment {
		v.JudgmentHash = ShortHex(hexOf(e.JudgmentHash), 12)
	}
	return v
}

func (r strategyReader) ReadEpochs(ctx context.Context, gen int64, limit int) (StrategyEpochsView, error) {
	g, err := r.generation(ctx, gen)
	if err != nil {
		return StrategyEpochsView{}, err
	}
	v := StrategyEpochsView{Generation: g, Epochs: []EpochView{}}
	if g == 0 {
		return v, nil
	}
	eps, err := r.st.ListEpochs(ctx, r.profile, g)
	if tampered(err) {
		return StrategyEpochsView{Generation: g, Epochs: []EpochView{}, Tampered: true, Note: StrategyTamperedNote}, nil
	}
	if err != nil {
		return v, err
	}
	limit = ClampStrategyLimit(limit)
	v.Total = len(eps)
	if len(eps) > limit {
		eps, v.Truncated = eps[len(eps)-limit:], true
	}
	reasons := r.badReasons(ctx, g, eps)
	for _, e := range eps {
		v.Epochs = append(v.Epochs, r.withReason(epochView(e), reasons, g))
	}
	return v, nil
}

func (r strategyReader) ReadDirections(ctx context.Context, gen int64, limit int) (StrategyDirectionsView, error) {
	g, err := r.generation(ctx, gen)
	if err != nil {
		return StrategyDirectionsView{}, err
	}
	v := StrategyDirectionsView{Generation: g, Directions: []DirectionView{}}
	if g == 0 {
		return v, nil
	}
	dirs, err := r.st.ListDirections(ctx, r.profile, g)
	if tampered(err) {
		return StrategyDirectionsView{Generation: g, Directions: []DirectionView{}, Tampered: true, Note: StrategyTamperedNote}, nil
	}
	if err != nil {
		return v, err
	}
	limit = ClampStrategyLimit(limit)
	v.Total = len(dirs)
	if len(dirs) > limit {
		dirs, v.Truncated = dirs[len(dirs)-limit:], true
	}
	for _, d := range dirs {
		v.Directions = append(v.Directions, DirectionView{ID: RedactText(d.ID, types.MaxIDLen), State: string(d.State),
			Title: RedactText(d.Title, strategyTitleCap), Proposer: RedactText(d.ProposerSession, 32), Session: RedactText(d.SessionID, 32),
			CreatedEpoch: d.CreatedEpoch, Key: ShortHex(hexOf(d.KeyHash), 12)})
	}
	return v, nil
}

func hexOf(h [32]byte) string { return hex.EncodeToString(h[:]) }
