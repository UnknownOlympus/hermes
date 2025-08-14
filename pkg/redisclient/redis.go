package redisclient

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// NewClient creates and checks new connection with redis.
func NewClient(ctx context.Context, addr string, timeout time.Duration) (*redis.Client, error) {
	// parse connection address.
	opts, err := redis.ParseURL(addr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse redis address: %w", err)
	}

	// create a new client.
	client := redis.NewClient(opts)

	// Check connection via PING
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	err = client.Ping(ctx).Err()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to redis: %w", err)
	}

	return client, nil
}
