package modelgateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type ConcurrencyPermit interface {
	Acquire(ctx context.Context, key string, maxConcurrency int) (permit string, ok bool, err error)

	Release(ctx context.Context, key, permit string)
}

type RequestCharger interface {
	ChargeModelRequest(ctx context.Context, key string, maxRequests int64) (count int64, ok bool, err error)
}

type MemoryQuota struct {
	mu       sync.Mutex
	inflight map[string]map[string]bool
}

func NewMemoryQuota() *MemoryQuota {
	return &MemoryQuota{inflight: map[string]map[string]bool{}}
}

func (m *MemoryQuota) Acquire(ctx context.Context, key string, maxConcurrency int) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.inflight[key]) >= maxConcurrency {
		return "", false, nil
	}
	permit := randomPermit()
	if m.inflight[key] == nil {
		m.inflight[key] = map[string]bool{}
	}
	m.inflight[key][permit] = true
	return permit, true, nil
}

func (m *MemoryQuota) Release(ctx context.Context, key, permit string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if set := m.inflight[key]; set != nil {
		delete(set, permit)
	}
}

var _ ConcurrencyPermit = (*MemoryQuota)(nil)

type RedisQuota struct {
	rdb redis.Cmdable
	ttl time.Duration
}

func NewRedisQuota(rdb redis.Cmdable) *RedisQuota {
	return &RedisQuota{rdb: rdb, ttl: time.Hour}
}

func NewRedisQuotaFromURL(rawURL string) (*RedisQuota, error) {
	opt, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("modelgateway: parse redis url: %w", err)
	}
	return NewRedisQuota(redis.NewClient(opt)), nil
}

const acquireConcurrencyLua = `
local key = KEYS[1]
local permit = ARGV[1]
local maxcon = tonumber(ARGV[2])
local ttl = tonumber(ARGV[3])
-- remove expired members first so they never count
redis.call('ZREMRANGEBYSCORE', key, '-inf', ARGV[4])
local n = redis.call('ZCARD', key)
if n >= maxcon then
  return 0
end
redis.call('ZADD', key, ttl, permit)
return 1
`

const releaseConcurrencyLua = `
redis.call('ZREM', KEYS[1], ARGV[1])
return 1
`

func (q *RedisQuota) Acquire(ctx context.Context, key string, maxConcurrency int) (string, bool, error) {
	if q.rdb == nil {
		return "", false, ErrQuotaUnconfigured
	}
	ifc := "quota:inflight:" + key
	permit := randomPermit()
	members, err := q.rdb.Eval(ctx, acquireConcurrencyLua,
		[]string{ifc},
		permit, maxConcurrency, time.Now().Add(q.ttl).UnixMilli(), time.Now().UnixMilli()).Result()
	if err != nil {
		return "", false, err
	}
	if members != int64(1) && members != 1 {
		return "", false, nil
	}
	return permit, true, nil
}

func (q *RedisQuota) Release(ctx context.Context, key, permit string) {
	if q.rdb == nil || permit == "" {
		return
	}
	_, _ = q.rdb.Eval(ctx, releaseConcurrencyLua,
		[]string{"quota:inflight:" + key}, permit).Result()
}

var ErrQuotaUnconfigured = &quotaErr{"modelgateway: redis quota client not configured"}

type quotaErr struct{ msg string }

func (e *quotaErr) Error() string { return e.msg }

func randomPermit() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {

		return hex.EncodeToString([]byte(time.Now().Format("150405.000000000")))
	}
	return hex.EncodeToString(b[:])
}
