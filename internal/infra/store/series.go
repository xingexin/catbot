package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// TimeSeriesStore is optional so existing Store wrappers need not implement
// unrelated time-range operations. This storage contract has no domain imports.
type TimeSeriesStore interface {
	ConfigureSeries(context.Context, string, string, SeriesPolicy, time.Time) error
	AppendSeries(context.Context, string, string, SeriesEntry, SeriesPolicy, time.Time) error
	QuerySeries(context.Context, string, string, SeriesQuery) (SeriesPage, error)
	PruneSeries(context.Context, string, time.Time) error
}

// seriesPruneBatchSize is the excess record count that triggers quantity trimming.
// Messages are still persisted synchronously; this is not a write buffer.
const seriesPruneBatchSize = 100

type SeriesPolicy struct {
	Retention  time.Duration
	MaxRecords int
}

type SeriesEntry struct {
	ID, Source, Text string
	Time             time.Time
	Data             json.RawMessage
}

type SeriesQuery struct {
	From, Until, Now time.Time
	BeforeTime       time.Time
	BeforeID         string
	Source, Keyword  string
	Limit            int
}

type SeriesPage struct {
	Entries                                     []SeriesEntry
	HasMore                                     bool
	RecordedFrom, AvailableFrom, AvailableUntil time.Time
	DroppedBefore                               time.Time
	Policy                                      SeriesPolicy
}

func (s *Postgres) migrateSeries(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackRecordTransaction(ctx, tx)
	// PostgreSQL IF NOT EXISTS alone does not serialize concurrent first startup.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('catbot-time-series-migration:' || current_schema(),0))`); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
	CREATE TABLE IF NOT EXISTS time_series_scopes (
		namespace text NOT NULL, scope text NOT NULL, recorded_from timestamptz NOT NULL,
		retention_seconds bigint NOT NULL CHECK (retention_seconds > 0),
		max_records integer NOT NULL CHECK (max_records > 0), dropped_before timestamptz,
		PRIMARY KEY(namespace, scope)
	);
	CREATE TABLE IF NOT EXISTS time_series_entries (
		namespace text NOT NULL, scope text NOT NULL, id text NOT NULL,
		occurred_at timestamptz NOT NULL, source text NOT NULL, search_text text NOT NULL,
		data jsonb NOT NULL, PRIMARY KEY(namespace, scope, id),
		FOREIGN KEY(namespace, scope) REFERENCES time_series_scopes(namespace, scope) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS time_series_range_idx
		ON time_series_entries(namespace, scope, occurred_at DESC, id COLLATE "C" DESC);
	`)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func validateSeries(namespace, scope string, policy SeriesPolicy) error {
	if namespace == "" || scope == "" || policy.Retention < time.Second || policy.MaxRecords < 1 {
		return errors.New("invalid time series scope or retention policy")
	}
	return nil
}

func (s *Postgres) ConfigureSeries(ctx context.Context, namespace, scope string, policy SeriesPolicy, now time.Time) error {
	return s.writeSeries(ctx, namespace, scope, nil, policy, now, true)
}

func (s *Postgres) AppendSeries(ctx context.Context, namespace, scope string, entry SeriesEntry, policy SeriesPolicy, now time.Time) error {
	return s.writeSeries(ctx, namespace, scope, &entry, policy, now, false)
}

func (s *Postgres) writeSeries(ctx context.Context, namespace, scope string, entry *SeriesEntry, policy SeriesPolicy, now time.Time, configure bool) error {
	if err := validateSeries(namespace, scope, policy); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackRecordTransaction(ctx, tx)
	if _, err := tx.Exec(ctx, `INSERT INTO time_series_scopes(namespace,scope,recorded_from,retention_seconds,max_records)
		VALUES($1,$2,$3,$4,$5) ON CONFLICT(namespace,scope) DO NOTHING`, namespace, scope, now, int64(policy.Retention/time.Second), policy.MaxRecords); err != nil {
		return err
	}
	var seconds int64
	var maxRecords int
	if err := tx.QueryRow(ctx, `SELECT retention_seconds,max_records FROM time_series_scopes
		WHERE namespace=$1 AND scope=$2 FOR UPDATE`, namespace, scope).Scan(&seconds, &maxRecords); err != nil {
		return err
	}
	trimToTarget := configure && policy.MaxRecords < maxRecords
	if configure && (seconds != int64(policy.Retention/time.Second) || maxRecords != policy.MaxRecords) {
		seconds, maxRecords = int64(policy.Retention/time.Second), policy.MaxRecords
		if _, err := tx.Exec(ctx, `UPDATE time_series_scopes SET retention_seconds=$3,max_records=$4 WHERE namespace=$1 AND scope=$2`, namespace, scope, seconds, maxRecords); err != nil {
			return err
		}
	}
	// A stale in-flight append cannot restore an older retention policy.
	if entry != nil && !entry.Time.Before(now.Add(-time.Duration(seconds)*time.Second)) {
		if _, err := tx.Exec(ctx, `INSERT INTO time_series_entries(namespace,scope,id,occurred_at,source,search_text,data)
			VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(namespace,scope,id) DO NOTHING`,
			namespace, scope, entry.ID, entry.Time, entry.Source, entry.Text, []byte(entry.Data)); err != nil {
			return err
		}
	}
	if err := pruneSeriesScope(ctx, tx, namespace, scope, now.Add(-time.Duration(seconds)*time.Second), maxRecords, trimToTarget); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// pruneSeriesScope always removes expired messages. Quantity trimming waits for a
// full batch of excess, except when an administrator lowers the retention target.
// The caller holds the scope row lock so appends, policy changes and pruning agree.
func pruneSeriesScope(ctx context.Context, tx pgx.Tx, namespace, scope string, cutoff time.Time, maxRecords int, trimToTarget bool) error {
	_, err := tx.Exec(ctx, `WITH overflow AS MATERIALIZED (
		SELECT id FROM time_series_entries WHERE namespace=$1 AND scope=$2 AND occurred_at >= $3
		ORDER BY occurred_at DESC,id COLLATE "C" DESC OFFSET $4
	), removed AS (
		DELETE FROM time_series_entries WHERE namespace=$1 AND scope=$2 AND
		(occurred_at < $3 OR (($5::boolean OR (SELECT count(*) FROM overflow) >= $6)
			AND id IN (SELECT id FROM overflow))) RETURNING occurred_at
	) UPDATE time_series_scopes SET dropped_before=GREATEST(dropped_before,(SELECT max(occurred_at) FROM removed))
		WHERE namespace=$1 AND scope=$2 AND EXISTS (SELECT 1 FROM removed)`,
		namespace, scope, cutoff, maxRecords, trimToTarget, seriesPruneBatchSize)
	return err
}

func (s *Postgres) QuerySeries(ctx context.Context, namespace, scope string, q SeriesQuery) (SeriesPage, error) {
	page := SeriesPage{Entries: []SeriesEntry{}}
	if q.Limit < 1 || q.Limit > 201 || namespace == "" || scope == "" {
		return page, errors.New("invalid time series query")
	}
	// Metadata, range and page come from one snapshot while pruning/appends run.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return page, err
	}
	defer rollbackRecordTransaction(ctx, tx)
	var seconds int64
	var dropped *time.Time
	err = tx.QueryRow(ctx, `SELECT recorded_from,retention_seconds,max_records,dropped_before
		FROM time_series_scopes WHERE namespace=$1 AND scope=$2`, namespace, scope).
		Scan(&page.RecordedFrom, &seconds, &page.Policy.MaxRecords, &dropped)
	if errors.Is(err, pgx.ErrNoRows) {
		return page, nil
	}
	if err != nil {
		return page, err
	}
	page.Policy.Retention = time.Duration(seconds) * time.Second
	if dropped != nil {
		page.DroppedBefore = *dropped
	}
	cutoff := q.Now.Add(-page.Policy.Retention)
	if q.From.Before(cutoff) {
		q.From = cutoff
	}
	var first, last *time.Time
	if err := tx.QueryRow(ctx, `SELECT min(occurred_at),max(occurred_at) FROM time_series_entries
		WHERE namespace=$1 AND scope=$2 AND occurred_at >= $3 AND occurred_at <= $4`, namespace, scope, cutoff, q.Until).Scan(&first, &last); err != nil {
		return page, err
	}
	if first != nil {
		page.AvailableFrom, page.AvailableUntil = *first, *last
	}
	var cursorTime any
	if !q.BeforeTime.IsZero() {
		cursorTime = q.BeforeTime
	}
	rows, err := tx.Query(ctx, `SELECT id,occurred_at,source,search_text,data FROM time_series_entries
		WHERE namespace=$1 AND scope=$2 AND occurred_at >= $3 AND occurred_at <= $4
		AND ($5::timestamptz IS NULL OR (occurred_at,id COLLATE "C") < ($5,$6::text COLLATE "C"))
		AND ($7::text='' OR source=$7) AND ($8::text='' OR strpos(lower(search_text),lower($8)) > 0)
		ORDER BY occurred_at DESC,id COLLATE "C" DESC LIMIT $9`,
		namespace, scope, q.From, q.Until, cursorTime, q.BeforeID, q.Source, q.Keyword, q.Limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var entry SeriesEntry
		if err := rows.Scan(&entry.ID, &entry.Time, &entry.Source, &entry.Text, &entry.Data); err != nil {
			return page, err
		}
		page.Entries = append(page.Entries, entry)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Entries) > q.Limit {
		page.HasMore = true
		page.Entries = page.Entries[:q.Limit]
	}
	return page, tx.Commit(ctx)
}

func (s *Postgres) PruneSeries(ctx context.Context, namespace string, now time.Time) error {
	// Policy changes and writes serialize per scope; a row lock also prevents a
	// concurrent append from undoing the cleanup coverage marker.
	rows, err := s.pool.Query(ctx, `SELECT scope FROM time_series_scopes WHERE namespace=$1`, namespace)
	if err != nil {
		return err
	}
	var scopes []string
	for rows.Next() {
		var scope string
		if err := rows.Scan(&scope); err != nil {
			rows.Close()
			return err
		}
		scopes = append(scopes, scope)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, scope := range scopes {
		if err := s.pruneSeries(ctx, namespace, scope, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Postgres) pruneSeries(ctx context.Context, namespace, scope string, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackRecordTransaction(ctx, tx)
	var seconds int64
	var maxRecords int
	if err := tx.QueryRow(ctx, `SELECT retention_seconds,max_records FROM time_series_scopes
		WHERE namespace=$1 AND scope=$2 FOR UPDATE`, namespace, scope).Scan(&seconds, &maxRecords); err != nil {
		return err
	}
	if err := pruneSeriesScope(ctx, tx, namespace, scope, now.Add(-time.Duration(seconds)*time.Second), maxRecords, false); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
