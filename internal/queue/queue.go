package queue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const WorkStream = "agent:runs:queue"

const WakeChannel = "agent:runs:queue:wake"

const defaultReclaimMinIdle = time.Second

const maxReclaimBatch int64 = 100

type Message struct {
	ID    string
	RunID string
}

type Client struct {
	client *redis.Client
}

func New(redisURL string) (*Client, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("queue: parse redis url: %w", err)
	}
	return &Client{client: redis.NewClient(opts)}, nil
}

func (c *Client) Close() error {
	return c.client.Close()
}

func (c *Client) Ping(ctx context.Context) error {
	if err := c.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("queue: ping: %w", err)
	}
	return nil
}

func (c *Client) Reset(ctx context.Context) error {
	if err := c.client.Del(ctx, WorkStream).Err(); err != nil {
		return fmt.Errorf("queue: reset: %w", err)
	}
	if err := c.client.Del(ctx, WakeChannel).Err(); err != nil {
		return fmt.Errorf("queue: reset wake: %w", err)
	}
	return nil
}

func (c *Client) EnsureGroup(ctx context.Context, group string) error {
	err := c.client.XGroupCreateMkStream(ctx, WorkStream, group, "0").Err()
	if err != nil && strings.Contains(err.Error(), "BUSYGROUP") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("queue: ensure group %q: %w", group, err)
	}
	return nil
}

func (c *Client) Publish(ctx context.Context, runID string) error {
	if _, err := c.client.XAdd(ctx, &redis.XAddArgs{
		Stream: WorkStream,
		Values: map[string]any{"run_id": runID},
	}).Result(); err != nil {
		return fmt.Errorf("queue: publish %q: %w", runID, err)
	}
	if err := c.client.Publish(ctx, WakeChannel, "wake").Err(); err != nil {
		return fmt.Errorf("queue: publish wake: %w", err)
	}
	return nil
}

func (c *Client) Next(ctx context.Context, group, consumer string, block time.Duration) (Message, bool, error) {
	streams, err := c.client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    group,
		Consumer: consumer,
		Streams:  []string{WorkStream, ">"},
		Count:    1,
		Block:    block,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return Message{}, false, nil
	}
	if err != nil {
		return Message{}, false, fmt.Errorf("queue: next: %w", err)
	}
	for _, st := range streams {
		for _, m := range st.Messages {
			if runID, ok := m.Values["run_id"].(string); ok {
				return Message{ID: m.ID, RunID: runID}, true, nil
			}
		}
	}
	return Message{}, false, nil
}

func (c *Client) Ack(ctx context.Context, group, messageID string) error {
	if err := c.client.XAck(ctx, WorkStream, group, messageID).Err(); err != nil {
		return fmt.Errorf("queue: ack %s: %w", messageID, err)
	}
	return nil
}

func (c *Client) Reclaim(ctx context.Context, group, consumer string, minIdle time.Duration, count int64) ([]Message, error) {
	if minIdle <= 0 {
		minIdle = defaultReclaimMinIdle
	}
	if count <= 0 || count > maxReclaimBatch {
		count = maxReclaimBatch
	}
	var out []Message
	start := "0-0"
	for {
		msgs, next, err := c.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
			Stream:   WorkStream,
			Group:    group,
			Consumer: consumer,
			MinIdle:  minIdle,
			Start:    start,
			Count:    count,
		}).Result()
		if errors.Is(err, redis.Nil) {
			break
		}
		if err != nil {
			if unsupportedAutoClaim(err) {
				return c.reclaimLegacy(ctx, group, consumer, minIdle, count)
			}
			return out, fmt.Errorf("queue: reclaim: %w", err)
		}
		for _, m := range msgs {
			if runID, ok := m.Values["run_id"].(string); ok {
				out = append(out, Message{ID: m.ID, RunID: runID})
			}
		}
		start = next
		if next == "0-0" || len(msgs) == 0 {
			break
		}
	}
	return out, nil
}

func (c *Client) reclaimLegacy(ctx context.Context, group, consumer string, minIdle time.Duration, count int64) ([]Message, error) {
	pending, err := c.client.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: WorkStream,
		Group:  group,
		Start:  "-",
		End:    "+",
		Count:  count,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("queue: reclaim pending: %w", err)
	}
	ids := make([]string, 0, len(pending))
	for _, entry := range pending {
		if entry.Idle >= minIdle {
			ids = append(ids, entry.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	msgs, err := c.client.XClaim(ctx, &redis.XClaimArgs{
		Stream: WorkStream, Group: group, Consumer: consumer, MinIdle: minIdle, Messages: ids,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("queue: reclaim claim: %w", err)
	}
	out := make([]Message, 0, len(msgs))
	for _, msg := range msgs {
		if runID, ok := msg.Values["run_id"].(string); ok {
			out = append(out, Message{ID: msg.ID, RunID: runID})
		}
	}
	return out, nil
}

func unsupportedAutoClaim(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unknown command") && strings.Contains(message, "xautoclaim")
}
