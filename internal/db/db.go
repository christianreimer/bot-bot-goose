// Package db owns the Postgres pool and the typed query layer.
//
// The plan calls for sqlc; for v0 we hand-roll the queries on pgx/v5 so the
// codebase compiles from a fresh checkout. The .sql files under db/queries/
// are the contract — when sqlc adoption lands, the function signatures here
// stay the same.
package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	*pgxpool.Pool
}

// Pool sizing for the launch. Defaults are the values the launch-capacity
// plan §1.2 calls out; they're overridable via env so a managed-Postgres
// plan with a lower connection ceiling can dial down without a rebuild.
// MinConns warms the pool so the first request after a quiet period doesn't
// pay a dial-cost; MaxConnIdleTime trims connections that have been parked
// too long.
const (
	defaultMaxConns        = 64
	defaultMinConns        = 8
	defaultMaxConnLifetime = time.Hour
	defaultMaxConnIdleTime = 10 * time.Minute
)

func Open(ctx context.Context, url string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse db url: %w", err)
	}
	cfg.MaxConns = envInt32("BBG_DB_MAX_CONNS", defaultMaxConns)
	cfg.MinConns = envInt32("BBG_DB_MIN_CONNS", defaultMinConns)
	cfg.MaxConnLifetime = defaultMaxConnLifetime
	cfg.MaxConnIdleTime = defaultMaxConnIdleTime
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("dial postgres: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &DB{pool}, nil
}

func envInt32(key string, def int32) int32 {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	return int32(n)
}

// ErrNotFound is returned by query helpers when zero rows are returned. It
// hides pgx.ErrNoRows from callers so they don't depend on the driver.
var ErrNotFound = errors.New("not found")

// IsNotFound reports whether err is or wraps either ErrNotFound or pgx.ErrNoRows.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, pgx.ErrNoRows)
}
