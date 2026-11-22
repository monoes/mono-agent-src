package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog"
)

// cronLogger sends robfig/cron's panic reports (message plus the panic value
// and stack) to the daemon log.
type cronLogger struct{ l zerolog.Logger }

func (c cronLogger) Info(msg string, kv ...interface{}) { c.l.Info().Fields(kv).Msg("cron: " + msg) }
func (c cronLogger) Error(err error, msg string, kv ...interface{}) {
	c.l.Error().Err(err).Fields(kv).Msg("cron: " + msg)
}

// Scheduler manages recurring cron-triggered workflow jobs.
type Scheduler struct {
	cron   *cron.Cron
	logger zerolog.Logger
}

func NewScheduler(logger zerolog.Logger) *Scheduler {
	return &Scheduler{
		cron:   cron.New(cron.WithSeconds(), cron.WithChain(cron.Recover(cronLogger{logger}))),
		logger: logger,
	}
}

// Start begins the scheduler.
func (s *Scheduler) Start() {
	s.cron.Start()
}

// Stop gracefully stops the scheduler.
func (s *Scheduler) Stop() context.Context {
	return s.cron.Stop()
}

// AddWorkflowJob adds a cron job and returns its entry ID.
// spec must be a 6-field cron expression (sec min hour dom month dow) or
// "@every Xm" etc; the underlying cron instance uses WithSeconds(), so
// standard 5-field expressions are rejected. spec may optionally start with
// "CRON_TZ=<zone> " to schedule in a specific timezone.
func (s *Scheduler) AddWorkflowJob(spec string, fn func()) (cron.EntryID, error) {
	id, err := s.cron.AddFunc(spec, func() {
		// A panic is logged with the job's spec, then the chain's Recover keeps the process alive.
		defer func() {
			if r := recover(); r != nil {
				s.logger.Error().Str("spec", spec).Interface("panic", r).Msg("scheduled job panicked")
				panic(r)
			}
		}()
		fn()
	})
	if err != nil {
		return 0, fmt.Errorf("invalid cron spec %q: %w", spec, err)
	}
	return id, nil
}

// NextRun is when the entry fires next (zero when it isn't scheduled).
func (s *Scheduler) NextRun(id cron.EntryID) time.Time {
	return s.cron.Entry(id).Next
}

// RemoveJob removes a cron job by entry ID.
func (s *Scheduler) RemoveJob(id cron.EntryID) {
	s.cron.Remove(id)
}

// NextPeriod calculates the next execution time for a recurring action within a time window.
func NextPeriod(start, end time.Time, pollIntervalMinutes int) *time.Time {
	if pollIntervalMinutes <= 0 {
		return nil
	}
	now := time.Now().Truncate(time.Minute)

	if now.Before(start) {
		return &start
	}
	if now.After(end) {
		return nil
	}

	minutesSinceStart := int(now.Sub(start).Minutes())
	nextInterval := ((minutesSinceStart / pollIntervalMinutes) + 1) * pollIntervalMinutes
	nextTime := start.Add(time.Duration(nextInterval) * time.Minute)

	if nextTime.After(end) {
		return nil
	}
	return &nextTime
}
