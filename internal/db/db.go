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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	*pgxpool.Pool
}

func Open(ctx context.Context, url string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse db url: %w", err)
	}
	cfg.MaxConns = 16
	cfg.MaxConnLifetime = time.Hour
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

// ErrNotFound is returned by query helpers when zero rows are returned. It
// hides pgx.ErrNoRows from callers so they don't depend on the driver.
var ErrNotFound = errors.New("not found")

// IsNotFound reports whether err is or wraps either ErrNotFound or pgx.ErrNoRows.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, pgx.ErrNoRows)
}
