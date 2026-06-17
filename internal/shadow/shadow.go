package shadow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// DP represents one datapoint in the shadow (dp_id → value).
type DP struct {
	DPID          int
	DesiredValue  json.RawMessage
	ReportedValue json.RawMessage
	UpdatedAt     time.Time
}

type Service struct {
	db    *pgxpool.Pool
	cache *redis.Client
}

func New(db *pgxpool.Pool, cache *redis.Client) *Service {
	return &Service{db: db, cache: cache}
}

func redisKey(tid, did string) string {
	return fmt.Sprintf("shadow:%s:%s", tid, did)
}

// UpdateReported updates reported state from a /up rpt message.
// All DP upserts are sent as a single batch (one TCP round-trip) instead
// of N sequential round-trips.
func (s *Service) UpdateReported(ctx context.Context, tid, did string, d json.RawMessage) error {
	var dps map[string]json.RawMessage
	if err := json.Unmarshal(d, &dps); err != nil {
		return fmt.Errorf("unmarshal rpt dps: %w", err)
	}
	if len(dps) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for dpIDStr, val := range dps {
		var dpID int
		fmt.Sscanf(dpIDStr, "%d", &dpID)
		if dpID < 1 {
			continue
		}
		batch.Queue(`
			INSERT INTO shadows (tid, did, dp_id, reported_value, updated_at)
			VALUES ($1, $2, $3, $4, NOW())
			ON CONFLICT (tid, did, dp_id) DO UPDATE
			  SET reported_value = EXCLUDED.reported_value,
			      updated_at     = NOW()
		`, tid, did, dpID, []byte(val))
	}

	br := s.db.SendBatch(ctx, batch)
	for i := 0; i < batch.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			br.Close()
			return err
		}
	}
	if err := br.Close(); err != nil {
		return err
	}

	return s.refreshCache(ctx, tid, did)
}

// HandleShadow updates Redis from a /shd retained message (full state snapshot).
func (s *Service) HandleShadow(ctx context.Context, tid, did string, dps json.RawMessage, rssi int, t int64) error {
	full := map[string]interface{}{
		"dps":  dps,
		"rssi": rssi,
		"t":    t,
	}
	b, err := json.Marshal(full)
	if err != nil {
		return err
	}
	return s.cache.Set(ctx, redisKey(tid, did), string(b), 24*time.Hour).Err()
}

// GetShadow returns the current shadow for a device from Redis (falls back to Postgres).
func (s *Service) GetShadow(ctx context.Context, tid, did string) (json.RawMessage, error) {
	val, err := s.cache.Get(ctx, redisKey(tid, did)).Result()
	if err == nil {
		return json.RawMessage(val), nil
	}
	return s.shadowFromDB(ctx, tid, did)
}

// SetDesired writes a desired state to Postgres and Redis, used when a command is issued.
func (s *Service) SetDesired(ctx context.Context, tid, did string, dpID int, value json.RawMessage) error {
	if _, err := s.db.Exec(ctx, `
		INSERT INTO shadows (tid, did, dp_id, desired_value, updated_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (tid, did, dp_id) DO UPDATE
		  SET desired_value = EXCLUDED.desired_value,
		      updated_at    = NOW()
	`, tid, did, dpID, []byte(value)); err != nil {
		return err
	}
	return s.refreshCache(ctx, tid, did)
}

func (s *Service) refreshCache(ctx context.Context, tid, did string) error {
	rows, err := s.db.Query(ctx, `
		SELECT dp_id, desired_value, reported_value FROM shadows
		 WHERE tid=$1 AND did=$2
	`, tid, did)
	if err != nil {
		return err
	}
	defer rows.Close()

	desired := map[string]json.RawMessage{}
	reported := map[string]json.RawMessage{}
	for rows.Next() {
		var dpID int
		var des, rep []byte
		rows.Scan(&dpID, &des, &rep)
		key := fmt.Sprintf("%d", dpID)
		if des != nil {
			desired[key] = des
		}
		if rep != nil {
			reported[key] = rep
		}
	}

	out, _ := json.Marshal(map[string]interface{}{
		"desired":  desired,
		"reported": reported,
	})
	return s.cache.Set(ctx, redisKey(tid, did), string(out), 24*time.Hour).Err()
}

func (s *Service) shadowFromDB(ctx context.Context, tid, did string) (json.RawMessage, error) {
	if err := s.refreshCache(ctx, tid, did); err != nil {
		return nil, err
	}
	val, err := s.cache.Get(ctx, redisKey(tid, did)).Result()
	if err != nil {
		return nil, err
	}
	return json.RawMessage(val), nil
}
