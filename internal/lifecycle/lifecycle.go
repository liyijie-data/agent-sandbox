package lifecycle

import (
	"sync"
	"time"

	"agent-platform/internal/contracts"
)

type Phase string

const (
	PhasePreparing  Phase = "preparing"
	PhaseExecuting  Phase = "executing"
	PhasePausing    Phase = "pausing"
	PhaseFinalizing Phase = "finalizing"
)

func ValidPhase(p string) bool {
	switch Phase(p) {
	case PhasePreparing, PhaseExecuting, PhasePausing, PhaseFinalizing:
		return true
	}
	return false
}

type Policy struct {
	QueueBudget time.Duration

	ExecutionBudget time.Duration

	TotalLifecycle time.Duration

	InputWaitBudget time.Duration

	MaxScheduleAttempts int

	ContentRetention  time.Duration
	MetadataRetention time.Duration
}

func DefaultPolicy() Policy {
	return Policy{
		QueueBudget:         10 * time.Minute,
		ExecutionBudget:     30 * time.Minute,
		TotalLifecycle:      48 * time.Hour,
		InputWaitBudget:     24 * time.Hour,
		MaxScheduleAttempts: 3,
		ContentRetention:    1 * time.Hour,
		MetadataRetention:   7 * 24 * time.Hour,
	}
}

func (p Policy) TotalLifecycleDeadline(createdAt time.Time) time.Time {
	return createdAt.Add(p.TotalLifecycle)
}

func (p Policy) InputDeadline(inputStartedAt, runTotalDeadline time.Time) time.Time {
	byWait := inputStartedAt.Add(p.InputWaitBudget)
	if byWait.Before(runTotalDeadline) {
		return byWait
	}
	return runTotalDeadline
}

type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

var SystemClock Clock = systemClock{}

type ControllableClock struct {
	mu      sync.Mutex
	current time.Time
}

func NewControllableClock(start time.Time) *ControllableClock {
	return &ControllableClock{current: start.UTC()}
}

func (c *ControllableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *ControllableClock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = c.current.Add(d)
	return c.current
}

func IsTerminalStatus(s contracts.RunStatus) bool { return s.IsTerminal() }

func StatusFromString(s string) contracts.RunStatus {
	return contracts.RunStatus(s)
}
