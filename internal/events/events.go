package events

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"agent-platform/internal/contracts"
	"agent-platform/internal/lifecycle"
	"agent-platform/internal/storage"
	"agent-platform/model"
)

const (
	PurposeEventStream = "run.events.stream"
	PurposeOutboxEvent = "run.events.outbox"
)

const MaxDeltaBytes = contracts.RuntimeEventDeltaMaxBytes

const (
	EventAgentToken      = contracts.EventAgentToken
	EventRuntimeProgress = contracts.EventRuntimeProgress
)

var (
	ErrInvalidEvent      = errors.New("events: invalid event")
	ErrNotWhitelisted    = errors.New("events: event type not whitelisted")
	ErrCapacity          = errors.New("events: event capacity exceeded")
	ErrTooLarge          = errors.New("events: event too large")
	ErrSourceSeqConflict = errors.New("events: source_seq conflict")
	ErrInvalidCursor     = errors.New("events: invalid cursor")
	ErrGap               = errors.New("events: stream epoch gap")
	ErrExpired           = errors.New("events: events expired")
)

type Service struct {
	cache     *storage.EventCache
	cipher    *storage.ContentCipher
	contents  model.RunContentDAO
	outbox    model.OutboxDAO
	retention time.Duration
}

func New(cache *storage.EventCache, cipher *storage.ContentCipher, store *model.Store, retention time.Duration) *Service {
	if retention <= 0 {
		retention = lifecycle.DefaultPolicy().ContentRetention
	}
	return &Service{
		cache:     cache,
		cipher:    cipher,
		contents:  store.DAOs().Contents,
		outbox:    store.DAOs().Outbox,
		retention: retention,
	}
}

type RunMeta struct {
	RunID        string
	Stage        int
	Fence        int64
	SourceSeq    int64
	HardDeadline time.Time
	TerminalAt   *time.Time
}

func (m RunMeta) ExpiresAt(retention time.Duration) time.Time {
	if m.TerminalAt != nil {
		return m.TerminalAt.Add(retention)
	}
	return m.HardDeadline
}

type Progress struct {
	Phase string `json:"phase"`
	Value int64  `json:"value"`
}

type envelope struct {
	Event    contracts.Event `json:"event"`
	Progress *Progress       `json:"progress,omitempty"`
}

type Cursor struct {
	runID string
	epoch int64
	seq   int64
}

func NewCursor(runID string, epoch, seq int64) Cursor {
	return Cursor{runID: runID, epoch: epoch, seq: seq}
}

func (c Cursor) RunID() string { return c.runID }

func (c Cursor) Epoch() int64 { return c.epoch }

func (c Cursor) Seq() int64 { return c.seq }

func (c Cursor) IsZero() bool { return c.runID == "" }

func (c Cursor) String() string {
	if c.runID == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%d:%d", c.runID, c.epoch, c.seq)))
}

func ParseCursor(raw string) (Cursor, error) {
	if raw == "" {
		return Cursor{}, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return Cursor{}, fmt.Errorf("events: %w: undecodable cursor", ErrInvalidCursor)
	}
	parts := strings.Split(string(b), ":")
	if len(parts) != 3 {
		return Cursor{}, fmt.Errorf("events: %w: malformed cursor", ErrInvalidCursor)
	}
	epoch, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || epoch < 1 {
		return Cursor{}, fmt.Errorf("events: %w: bad stream epoch", ErrInvalidCursor)
	}
	seq, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || seq < 0 {
		return Cursor{}, fmt.Errorf("events: %w: bad sequence", ErrInvalidCursor)
	}
	return Cursor{runID: parts[0], epoch: epoch, seq: seq}, nil
}

func mapCacheErr(err error) error {
	switch {
	case errors.Is(err, storage.ErrEventGap):
		return ErrGap
	case errors.Is(err, storage.ErrEventExpired):
		return ErrExpired
	case errors.Is(err, storage.ErrEventTooLarge):
		return ErrTooLarge
	case errors.Is(err, storage.ErrEventCapacity):
		return ErrCapacity
	case errors.Is(err, storage.ErrEventConflict):
		return ErrSourceSeqConflict
	default:
		return err
	}
}

func deterministicEventID(runID string, stage int, sourceSeq int64, etype contracts.EventType) string {
	h := sha256.New()
	h.Write([]byte(runID))
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(stage)))
	h.Write([]byte{0})
	h.Write([]byte(strconv.FormatInt(sourceSeq, 10)))
	h.Write([]byte{0})
	h.Write([]byte(etype))
	return uuid.NewSHA1(uuid.NameSpaceOID, h.Sum(nil)).String()
}
