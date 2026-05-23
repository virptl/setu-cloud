package cache

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// Connect parses the Redis URL, creates a client, and verifies connectivity.
func Connect(ctx context.Context, url string) (*redis.Client, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("redis.ParseURL: %w", err)
	}
	client := redis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis ping: %w", err)
	}
	return client, nil
}
