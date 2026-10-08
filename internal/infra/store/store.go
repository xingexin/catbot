package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("record not found")

// EventRecord is the storage transport shape; domain repositories map it to events.
type EventRecord struct {
	Sequence int64          `json:"sequence"`
	RunID    string         `json:"runId"`
	Type     string         `json:"type"`
	Data     map[string]any `json:"data"`
	Time     time.Time      `json:"time"`
}

type Store interface {
	Put(context.Context, string, string, any) error
	Get(context.Context, string, string, any) error
	List(context.Context, string) ([]json.RawMessage, error)
	Delete(context.Context, string, string) error
	Lock(context.Context, string) (func(), error)
	Append(context.Context, EventRecord) error
	Events(context.Context, string, int64) ([]EventRecord, error)
	Ping(context.Context) error
	Close()
}

func All[T any](ctx context.Context, s Store, kind string) ([]T, error) {
	raw, err := s.List(ctx, kind)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(raw))
	for _, b := range raw {
		var item T
		if err := json.Unmarshal(b, &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

type Postgres struct{ pool, locks *pgxpool.Pool }

func Open(ctx context.Context, url string) (*Postgres, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 32
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	lockCfg := cfg.Copy()
	lockCfg.MaxConns = 64
	locks, err := pgxpool.NewWithConfig(ctx, lockCfg)
	if err != nil {
		pool.Close()
		return nil, err
	}
	s := &Postgres{pool: pool, locks: locks}
	_, err = pool.Exec(ctx, `
	CREATE TABLE IF NOT EXISTS records (
		kind text NOT NULL, id text NOT NULL, data jsonb NOT NULL,
		updated_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(kind,id)
	);
	CREATE INDEX IF NOT EXISTS records_data_idx ON records USING gin(data);
	CREATE INDEX IF NOT EXISTS records_run_queue_idx ON records ((data->>'createdAt'), id)
		WHERE kind='run' AND data->>'status'='queued' AND id NOT LIKE 'background-%';
	CREATE TABLE IF NOT EXISTS run_events (
		sequence bigserial PRIMARY KEY, run_id text NOT NULL,
		type text NOT NULL, data jsonb NOT NULL, created_at timestamptz NOT NULL
	);
	CREATE INDEX IF NOT EXISTS run_events_run_idx ON run_events(run_id,sequence);
	`)
	if err != nil {
		pool.Close()
		locks.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return s, nil
}

func (s *Postgres) Put(ctx context.Context, kind, id string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO records(kind,id,data) VALUES($1,$2,$3)
	ON CONFLICT(kind,id) DO UPDATE SET data=excluded.data, updated_at=now()`, kind, id, b)
	return err
}
func (s *Postgres) Get(ctx context.Context, kind, id string, v any) error {
	var b []byte
	err := s.pool.QueryRow(ctx, "SELECT data FROM records WHERE kind=$1 AND id=$2", kind, id).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
func (s *Postgres) List(ctx context.Context, kind string) ([]json.RawMessage, error) {
	rows, err := s.pool.Query(ctx, "SELECT data FROM records WHERE kind=$1 ORDER BY updated_at,id", kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(b))
	}
	return out, rows.Err()
}
func (s *Postgres) Delete(ctx context.Context, kind, id string) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM records WHERE kind=$1 AND id=$2", kind, id)
	return err
}
func (s *Postgres) Lock(ctx context.Context, key string) (func(), error) {
	var conn *pgxpool.Conn
	for {
		var err error
		conn, err = s.locks.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		var acquired bool
		if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,0))", key).Scan(&acquired); err != nil {
			conn.Release()
			return nil, err
		}
		if acquired {
			break
		}
		conn.Release()
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(c, "SELECT pg_advisory_unlock(hashtextextended($1,0))", key); err != nil {
			_ = conn.Conn().Close(c)
		}
		conn.Release()
	}, nil
}
func (s *Postgres) Append(ctx context.Context, e EventRecord) error {
	b, err := json.Marshal(e.Data)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, "INSERT INTO run_events(run_id,type,data,created_at) VALUES($1,$2,$3,$4)", e.RunID, e.Type, b, e.Time)
	return err
}
func (s *Postgres) Events(ctx context.Context, id string, after int64) ([]EventRecord, error) {
	rows, err := s.pool.Query(ctx, "SELECT sequence,type,data,created_at FROM run_events WHERE run_id=$1 AND sequence>$2 ORDER BY sequence LIMIT 500", id, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EventRecord{}
	for rows.Next() {
		e := EventRecord{RunID: id}
		var b []byte
		if err := rows.Scan(&e.Sequence, &e.Type, &b, &e.Time); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &e.Data); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }
func (s *Postgres) Close()                         { s.locks.Close(); s.pool.Close() }

// Memory is an explicit test dependency; production always uses PostgreSQL.
type Memory struct {
	mu     sync.Mutex
	data   map[string]map[string]json.RawMessage
	events []EventRecord
	locks  map[string]chan struct{}
}

func NewMemory() *Memory {
	return &Memory{data: map[string]map[string]json.RawMessage{}, locks: map[string]chan struct{}{}}
}
func (s *Memory) Put(_ context.Context, k, id string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data[k] == nil {
		s.data[k] = map[string]json.RawMessage{}
	}
	s.data[k][id] = b
	return nil
}
func (s *Memory) Get(_ context.Context, k, id string, v any) error {
	s.mu.Lock()
	b, ok := s.data[k][id]
	s.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	return json.Unmarshal(b, v)
}
func (s *Memory) List(_ context.Context, k string) ([]json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := []string{}
	for id := range s.data[k] {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := []json.RawMessage{}
	for _, id := range ids {
		out = append(out, append(json.RawMessage(nil), s.data[k][id]...))
	}
	return out, nil
}
func (s *Memory) Delete(_ context.Context, k, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data[k], id)
	return nil
}
func (s *Memory) Lock(ctx context.Context, key string) (func(), error) {
	s.mu.Lock()
	ch := s.locks[key]
	if ch == nil {
		ch = make(chan struct{}, 1)
		s.locks[key] = ch
	}
	s.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (s *Memory) Append(_ context.Context, e EventRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.Sequence = int64(len(s.events) + 1)
	s.events = append(s.events, e)
	return nil
}
func (s *Memory) Events(_ context.Context, id string, after int64) ([]EventRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []EventRecord{}
	for _, e := range s.events {
		if e.RunID == id && e.Sequence > after {
			out = append(out, e)
		}
	}
	return out, nil
}
func (s *Memory) Ping(context.Context) error { return nil }
func (s *Memory) Close()                     {}
