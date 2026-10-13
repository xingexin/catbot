package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
)

type memorySeries struct {
	policy                      SeriesPolicy
	recordedFrom, droppedBefore time.Time
	entries                     map[string]SeriesEntry
}

func (s *Memory) ConfigureSeries(ctx context.Context, namespace, scope string, policy SeriesPolicy, now time.Time) error {
	return s.writeSeries(ctx, namespace, scope, nil, policy, now, true)
}

func (s *Memory) AppendSeries(ctx context.Context, namespace, scope string, entry SeriesEntry, policy SeriesPolicy, now time.Time) error {
	return s.writeSeries(ctx, namespace, scope, &entry, policy, now, false)
}

func (s *Memory) writeSeries(ctx context.Context, namespace, scope string, entry *SeriesEntry, policy SeriesPolicy, now time.Time, configure bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSeries(namespace, scope, policy); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.series == nil {
		s.series = map[string]map[string]*memorySeries{}
	}
	if s.series[namespace] == nil {
		s.series[namespace] = map[string]*memorySeries{}
	}
	series := s.series[namespace][scope]
	if series == nil {
		series = &memorySeries{policy: policy, recordedFrom: now, entries: map[string]SeriesEntry{}}
		s.series[namespace][scope] = series
	}
	trimToTarget := configure && policy.MaxRecords < series.policy.MaxRecords
	if configure {
		series.policy = policy
	}
	if entry != nil && !entry.Time.Before(now.Add(-series.policy.Retention)) {
		if _, exists := series.entries[entry.ID]; !exists {
			copy := *entry
			copy.Data = slices.Clone(entry.Data)
			series.entries[entry.ID] = copy
		}
	}
	pruneMemorySeries(series, now, trimToTarget)
	return nil
}

func sortedSeriesEntries(entries map[string]SeriesEntry) []SeriesEntry {
	out := make([]SeriesEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry)
	}
	slices.SortFunc(out, func(a, b SeriesEntry) int {
		if !a.Time.Equal(b.Time) {
			return b.Time.Compare(a.Time)
		}
		return strings.Compare(b.ID, a.ID)
	})
	return out
}

func pruneMemorySeries(series *memorySeries, now time.Time, trimToTarget bool) {
	cutoff := now.Add(-series.policy.Retention)
	remove := func(entry SeriesEntry) {
		delete(series.entries, entry.ID)
		if entry.Time.After(series.droppedBefore) {
			series.droppedBefore = entry.Time
		}
	}
	for _, entry := range series.entries {
		if entry.Time.Before(cutoff) {
			remove(entry)
		}
	}
	if !trimToTarget && len(series.entries)-series.policy.MaxRecords < seriesPruneBatchSize {
		return
	}
	for i, entry := range sortedSeriesEntries(series.entries) {
		if i >= series.policy.MaxRecords {
			remove(entry)
		}
	}
}

func (s *Memory) QuerySeries(ctx context.Context, namespace, scope string, q SeriesQuery) (SeriesPage, error) {
	page := SeriesPage{Entries: []SeriesEntry{}}
	if err := ctx.Err(); err != nil {
		return page, err
	}
	if q.Limit < 1 || q.Limit > 201 || namespace == "" || scope == "" {
		return page, errors.New("invalid time series query")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	series := s.series[namespace][scope]
	if series == nil {
		return page, nil
	}
	page.Policy, page.RecordedFrom, page.DroppedBefore = series.policy, series.recordedFrom, series.droppedBefore
	cutoff := q.Now.Add(-series.policy.Retention)
	if q.From.Before(cutoff) {
		q.From = cutoff
	}
	for _, entry := range sortedSeriesEntries(series.entries) {
		if entry.Time.Before(cutoff) || entry.Time.After(q.Until) {
			continue
		}
		if page.AvailableUntil.IsZero() {
			page.AvailableUntil = entry.Time
		}
		page.AvailableFrom = entry.Time
		if entry.Time.Before(q.From) || entry.Time.After(q.Until) {
			continue
		}
		if !q.BeforeTime.IsZero() && (entry.Time.After(q.BeforeTime) || (entry.Time.Equal(q.BeforeTime) && entry.ID >= q.BeforeID)) {
			continue
		}
		if q.Source != "" && entry.Source != q.Source {
			continue
		}
		if q.Keyword != "" && !strings.Contains(strings.ToLower(entry.Text), strings.ToLower(q.Keyword)) {
			continue
		}
		if len(page.Entries) == q.Limit {
			page.HasMore = true
			continue
		}
		entry.Data = slices.Clone(entry.Data)
		page.Entries = append(page.Entries, entry)
	}
	return page, nil
}

func (s *Memory) PruneSeries(ctx context.Context, namespace string, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, series := range s.series[namespace] {
		pruneMemorySeries(series, now, false)
	}
	return nil
}
