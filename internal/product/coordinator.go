package product

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/domain"
	"github.com/redis/go-redis/v9"
)

type RedisCoordinator struct {
	client       *redis.Client
	prefix       string
	resultTTL    time.Duration
	lockTTL      time.Duration
	pollInterval time.Duration
}

type coordinationResult struct {
	Fingerprint string       `json:"fingerprint"`
	Order       domain.Order `json:"order"`
}

func NewRedisCoordinator(client *redis.Client, prefix string) *RedisCoordinator {
	return &RedisCoordinator{
		client:       client,
		prefix:       prefix,
		resultTTL:    24 * time.Hour,
		lockTTL:      15 * time.Second,
		pollInterval: 20 * time.Millisecond,
	}
}

func (c *RedisCoordinator) Ping(ctx context.Context) error {
	if err := c.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrCoordinationUnavailable, err)
	}
	return nil
}

func (c *RedisCoordinator) Do(ctx context.Context, tenantID, key, fingerprint string, fn func(context.Context) (domain.Order, error)) (domain.Order, bool, error) {
	resultKey := c.prefix + ":result:" + tenantID + ":" + key
	lockKey := c.prefix + ":lock:" + tenantID + ":" + key
	token := randomToken()

	for {
		result, found, err := c.loadResult(ctx, resultKey)
		if err != nil {
			return domain.Order{}, false, err
		}
		if found {
			if result.Fingerprint != fingerprint {
				return domain.Order{}, false, ErrIdempotencyConflict
			}
			return result.Order, true, nil
		}

		acquired, err := c.client.SetNX(ctx, lockKey, token, c.lockTTL).Result()
		if err != nil {
			return domain.Order{}, false, fmt.Errorf("%w: %v", ErrCoordinationUnavailable, err)
		}
		if acquired {
			return c.execute(ctx, resultKey, lockKey, token, fingerprint, fn)
		}

		timer := time.NewTimer(c.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return domain.Order{}, false, ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *RedisCoordinator) execute(ctx context.Context, resultKey, lockKey, token, fingerprint string, fn func(context.Context) (domain.Order, error)) (domain.Order, bool, error) {
	defer c.release(lockKey, token)

	result, found, err := c.loadResult(ctx, resultKey)
	if err != nil {
		return domain.Order{}, false, err
	}
	if found {
		if result.Fingerprint != fingerprint {
			return domain.Order{}, false, ErrIdempotencyConflict
		}
		return result.Order, true, nil
	}

	order, err := fn(ctx)
	if err != nil {
		return domain.Order{}, false, err
	}
	encoded, err := json.Marshal(coordinationResult{Fingerprint: fingerprint, Order: order})
	if err != nil {
		return domain.Order{}, false, err
	}
	if err := c.client.Set(ctx, resultKey, encoded, c.resultTTL).Err(); err != nil {
		return domain.Order{}, false, fmt.Errorf("%w: result write failed: %v", ErrCoordinationUnavailable, err)
	}
	return order, false, nil
}

func (c *RedisCoordinator) loadResult(ctx context.Context, key string) (coordinationResult, bool, error) {
	encoded, err := c.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return coordinationResult{}, false, nil
	}
	if err != nil {
		return coordinationResult{}, false, fmt.Errorf("%w: result read failed: %v", ErrCoordinationUnavailable, err)
	}
	var result coordinationResult
	if err := json.Unmarshal(encoded, &result); err != nil {
		return coordinationResult{}, false, fmt.Errorf("decode coordination result: %w", err)
	}
	return result, true, nil
}

func (c *RedisCoordinator) release(lockKey, token string) {
	const releaseScript = `if redis.call("get", KEYS[1]) == ARGV[1] then return redis.call("del", KEYS[1]) else return 0 end`
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = c.client.Eval(ctx, releaseScript, []string{lockKey}, token).Err()
}

func randomToken() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}
