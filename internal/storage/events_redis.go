package storage

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	MaxEventBytes = 64*1024 + 8*1024

	MaxRunBytes = 16 * 1024 * 1024
)

var (
	ErrEventGap = errors.New("storage: event cache: stream epoch gap")

	ErrEventExpired = errors.New("storage: event cache: run events expired")

	ErrEventTooLarge = errors.New("storage: event cache: single entry exceeds byte cap")

	ErrEventCapacity = errors.New("storage: event cache: per-run byte cap exceeded")

	ErrEventConflict = errors.New("storage: event cache: producer seq reused with different content")
)

const (
	eventStreamPrefix = "agent:events:"
	eventMetaPrefix   = "agent:events:meta:"
)

func eventStreamKey(runID string) string { return eventStreamPrefix + runID }
func eventMetaKey(runID string) string   { return eventMetaPrefix + runID }

type EventEntry struct {
	StreamID   string
	Epoch      int64
	Seq        int64
	Type       string
	KeyVersion int
	Payload    []byte
}

type EventCacheStats struct {
	Epoch     int64
	Seq       int64
	Bytes     int64
	ExpiresAt time.Time
}

type EventCache struct {
	client *redis.Client
}

func NewEventCache(redisURL string) (*EventCache, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("storage: event cache: parse redis url: %w", err)
	}
	return &EventCache{client: redis.NewClient(opts)}, nil
}

func (c *EventCache) Close() error { return c.client.Close() }

func (c *EventCache) Ping(ctx context.Context) error {
	if err := c.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("storage: event cache: ping: %w", err)
	}
	return nil
}

func (c *EventCache) Reset(ctx context.Context, runID string) error {
	if err := c.client.Del(ctx, eventStreamKey(runID), eventMetaKey(runID)).Err(); err != nil {
		return fmt.Errorf("storage: event cache: reset %s: %w", runID, err)
	}
	return nil
}

func (c *EventCache) DropStream(ctx context.Context, runID string) error {
	if err := c.client.Del(ctx, eventStreamKey(runID)).Err(); err != nil {
		return fmt.Errorf("storage: event cache: drop stream %s: %w", runID, err)
	}
	return nil
}

func (c *EventCache) Append(ctx context.Context, runID, eventType string, keyVersion int, payload []byte, expiresAt time.Time) (int64, int64, error) {
	now := time.Now().UTC()
	if len(payload) > MaxEventBytes {
		return 0, 0, fmt.Errorf("storage: event cache: payload %d bytes exceeds %d: %w",
			len(payload), MaxEventBytes, ErrEventTooLarge)
	}
	if !expiresAt.After(now) {
		return 0, 0, fmt.Errorf("storage: event cache: expires_at %v is not in the future: %w",
			expiresAt, ErrEventExpired)
	}

	stream, meta := eventStreamKey(runID), eventMetaKey(runID)

	epoch, seq, bytes, expires := int64(1), int64(0), int64(0), expiresAt
	m, err := c.client.HGetAll(ctx, meta).Result()
	if err != nil {
		return 0, 0, fmt.Errorf("storage: event cache: meta read %s: %w", runID, err)
	}
	if len(m) > 0 {
		epoch = fieldInt(m, "epoch", 1)
		seq = fieldInt(m, "seq", 0)
		bytes = fieldInt(m, "bytes", 0)
		if stored := fieldInt(m, "expires_at", 0); stored > 0 {
			st := time.UnixMilli(stored)
			if st.Before(expires) {
				expires = st
			}
			if !st.After(now) {
				return 0, 0, fmt.Errorf("storage: event cache: run %s events expired at %v: %w",
					runID, st, ErrEventExpired)
			}
		}
	}

	first, err := c.firstEpoch(ctx, stream)
	if err != nil {
		return 0, 0, err
	}
	switch {
	case len(m) == 0 && first == 0:

	case len(m) > 0 && first != epoch:
		epoch++
		bytes = 0
		if err := c.client.Del(ctx, stream).Err(); err != nil {
			return 0, 0, fmt.Errorf("storage: event cache: drop stale stream %s: %w", runID, err)
		}
	case len(m) == 0 && first != 0:
		if err := c.client.Del(ctx, stream).Err(); err != nil {
			return 0, 0, fmt.Errorf("storage: event cache: drop orphan stream %s: %w", runID, err)
		}
	}

	if bytes+int64(len(payload)) > MaxRunBytes {
		return 0, 0, fmt.Errorf("storage: event cache: run %s event ciphertext %d+%d exceeds %d: %w",
			runID, bytes, len(payload), MaxRunBytes, ErrEventCapacity)
	}

	seq++
	id := fmt.Sprintf("%d-0", seq)
	if _, err := c.client.XAdd(ctx, &redis.XAddArgs{
		Stream: stream,
		ID:     id,
		Values: map[string]any{
			"epoch":   epoch,
			"seq":     seq,
			"type":    eventType,
			"kv":      keyVersion,
			"payload": payload,
		},
	}).Result(); err != nil {
		return 0, 0, fmt.Errorf("storage: event cache: xadd %s: %w", runID, err)
	}
	bytes += int64(len(payload))
	if err := c.client.HSet(ctx, meta, map[string]any{
		"epoch":      epoch,
		"seq":        seq,
		"bytes":      bytes,
		"expires_at": expires.UnixMilli(),
	}).Err(); err != nil {
		return 0, 0, fmt.Errorf("storage: event cache: meta write %s: %w", runID, err)
	}
	if err := c.applyTTL(ctx, stream, expires); err != nil {
		return 0, 0, err
	}
	if err := c.applyTTL(ctx, meta, expires); err != nil {
		return 0, 0, err
	}
	return seq, epoch, nil
}

func (c *EventCache) Read(ctx context.Context, runID string, afterEpoch, afterSeq int64, count int64, block time.Duration) ([]EventEntry, error) {
	stream, meta := eventStreamKey(runID), eventMetaKey(runID)
	m, err := c.client.HGetAll(ctx, meta).Result()
	if err != nil {
		return nil, fmt.Errorf("storage: event cache: meta read %s: %w", runID, err)
	}
	if len(m) == 0 {

		return nil, nil
	}
	epoch := fieldInt(m, "epoch", 1)
	if afterEpoch != 0 && epoch != afterEpoch {
		return nil, fmt.Errorf("storage: event cache: cursor epoch %d != stream epoch %d: %w",
			afterEpoch, epoch, ErrEventGap)
	}
	if ex := fieldInt(m, "expires_at", 0); ex > 0 && nowUnixMilli() >= ex {
		return nil, fmt.Errorf("storage: event cache: run %s events expired at %d: %w",
			runID, ex, ErrEventExpired)
	}
	if count <= 0 || count > 100 {
		count = 100
	}

	xblock := block
	if xblock <= 0 {
		xblock = -1
	}
	res, err := c.client.XRead(ctx, &redis.XReadArgs{
		Streams: []string{stream, fmt.Sprintf("%d-0", afterSeq)},
		Count:   count,
		Block:   xblock,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("storage: event cache: xread %s: %w", runID, err)
	}
	var out []EventEntry
	for _, st := range res {
		for _, msg := range st.Messages {
			e, perr := parseEventEntry(msg)
			if perr != nil {
				return nil, fmt.Errorf("storage: event cache: %s: %w", runID, perr)
			}
			if e.Epoch != epoch {
				return nil, fmt.Errorf("storage: event cache: entry epoch %d != stream epoch %d: %w",
					e.Epoch, epoch, ErrEventGap)
			}
			out = append(out, e)
		}
	}
	return out, nil
}

func (c *EventCache) Stats(ctx context.Context, runID string) (EventCacheStats, error) {
	var s EventCacheStats
	m, err := c.client.HGetAll(ctx, eventMetaKey(runID)).Result()
	if err != nil {
		return s, fmt.Errorf("storage: event cache: stats %s: %w", runID, err)
	}
	if len(m) == 0 {
		return s, nil
	}
	s.Epoch = fieldInt(m, "epoch", 0)
	s.Seq = fieldInt(m, "seq", 0)
	s.Bytes = fieldInt(m, "bytes", 0)
	if ex := fieldInt(m, "expires_at", 0); ex > 0 {
		s.ExpiresAt = time.UnixMilli(ex)
	}
	return s, nil
}

const eventDedupPrefix = "agent:events:dedup:"

func eventDedupKey(runID, executionID string, sourceSeq int64) string {
	return eventDedupPrefix + runID + ":" + executionID + ":" + strconv.FormatInt(sourceSeq, 10)
}

func (c *EventCache) ProducerSeqClaim(ctx context.Context, runID, executionID string, sourceSeq int64, hash string, ttl time.Duration) (bool, error) {
	key := eventDedupKey(runID, executionID, sourceSeq)
	ok, err := c.client.SetNX(ctx, key, hash, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("storage: event cache: producer claim %s: %w", runID, err)
	}
	if ok {
		return true, nil
	}
	existing, err := c.client.Get(ctx, key).Result()
	if err != nil {
		return false, fmt.Errorf("storage: event cache: producer claim read %s: %w", runID, err)
	}
	if existing == hash {
		return false, nil
	}
	return false, fmt.Errorf("storage: event cache: %s producer seq %d: %w", runID, sourceSeq, ErrEventConflict)
}

func (c *EventCache) ReleaseProducerSeq(ctx context.Context, runID, executionID string, sourceSeq int64) error {
	if err := c.client.Del(ctx, eventDedupKey(runID, executionID, sourceSeq)).Err(); err != nil {
		return fmt.Errorf("storage: event cache: release producer claim %s: %w", runID, err)
	}
	return nil
}

func (c *EventCache) firstEpoch(ctx context.Context, stream string) (int64, error) {
	msgs, err := c.client.XRangeN(ctx, stream, "-", "+", 1).Result()
	if err != nil {
		return 0, fmt.Errorf("storage: event cache: xrange %s: %w", stream, err)
	}
	if len(msgs) == 0 {
		return 0, nil
	}
	e, err := parseEventEntry(msgs[0])
	if err != nil {
		return 0, err
	}
	return e.Epoch, nil
}

func (c *EventCache) applyTTL(ctx context.Context, key string, expires time.Time) error {
	ttl := time.Until(expires)
	if ttl <= 0 {
		return nil
	}
	cur, err := c.client.PTTL(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("storage: event cache: pttl %s: %w", key, err)
	}
	if cur < 0 || ttl < cur {
		if err := c.client.PExpire(ctx, key, ttl).Err(); err != nil {
			return fmt.Errorf("storage: event cache: pexpire %s: %w", key, err)
		}
	}
	return nil
}

func parseEventEntry(msg redis.XMessage) (EventEntry, error) {
	e := EventEntry{StreamID: msg.ID}
	epoch, err := fieldIntVal(msg.Values["epoch"])
	if err != nil {
		return e, fmt.Errorf("entry %s: bad epoch: %w", msg.ID, err)
	}
	e.Epoch = epoch
	seq, err := fieldIntVal(msg.Values["seq"])
	if err != nil {
		return e, fmt.Errorf("entry %s: bad seq: %w", msg.ID, err)
	}
	e.Seq = seq
	e.Type, _ = msg.Values["type"].(string)
	if kv, err := fieldIntVal(msg.Values["kv"]); err == nil {
		e.KeyVersion = int(kv)
	}
	switch p := msg.Values["payload"].(type) {
	case string:
		e.Payload = []byte(p)
	case []byte:
		e.Payload = p
	default:
		return e, fmt.Errorf("entry %s: payload missing", msg.ID)
	}
	return e, nil
}

func fieldInt(m map[string]string, key string, def int64) int64 {
	if v, ok := m[key]; ok && v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

func fieldIntVal(v any) (int64, error) {
	s, ok := v.(string)
	if !ok {
		return 0, fmt.Errorf("not an integer field")
	}
	return strconv.ParseInt(s, 10, 64)
}

func nowUnixMilli() int64 { return time.Now().UTC().UnixMilli() }
