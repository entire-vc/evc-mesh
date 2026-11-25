package service

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lib/pq"
)

// MemoryDetailTimeoutError is separate from an empty result: a caller may retry,
// but must never mistake an interrupted query for a complete page.
type MemoryDetailTimeoutError struct{}

func (*MemoryDetailTimeoutError) Error() string { return MemoryReasonDetailTimeout }

func memoryDetail[T any](ctx context.Context, s *memoryAnalyticsService, read func(*memoryAnalyticsService) (*MemoryList[T], error)) (*MemoryList[T], error) {
	tx, err := s.pool.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, setTimeoutErr := tx.ExecContext(ctx, `SET LOCAL statement_timeout = '5s'`); setTimeoutErr != nil {
		return nil, setTimeoutErr
	}
	result, err := read(&memoryAnalyticsService{db: tx})
	if err != nil {
		var pgErr *pq.Error
		if errors.As(err, &pgErr) && pgErr.Code == "57014" && ctx.Err() == nil {
			return nil, &MemoryDetailTimeoutError{}
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *memoryAnalyticsService) EmptyQueries(ctx context.Context, f MemoryAnalyticsFilter, p MemoryListParams) (*MemoryList[MemoryEmptyQuery], error) {
	return memoryDetail(ctx, s, func(s *memoryAnalyticsService) (*MemoryList[MemoryEmptyQuery], error) {
		return s.emptyQueries(ctx, f, p)
	})
}

func (s *memoryAnalyticsService) TopRecords(ctx context.Context, f MemoryAnalyticsFilter, p MemoryListParams) (*MemoryList[MemoryRecordRow], error) {
	return memoryDetail(ctx, s, func(s *memoryAnalyticsService) (*MemoryList[MemoryRecordRow], error) {
		return s.topRecords(ctx, f, p)
	})
}

func (s *memoryAnalyticsService) DeadRecords(ctx context.Context, f MemoryAnalyticsFilter, p MemoryListParams) (*MemoryList[MemoryRecordRow], error) {
	return memoryDetail(ctx, s, func(s *memoryAnalyticsService) (*MemoryList[MemoryRecordRow], error) {
		return s.deadRecords(ctx, f, p)
	})
}

func (s *memoryAnalyticsService) Duplicates(ctx context.Context, f MemoryAnalyticsFilter, p MemoryListParams) (*MemoryList[MemoryRecordRow], error) {
	return memoryDetail(ctx, s, func(s *memoryAnalyticsService) (*MemoryList[MemoryRecordRow], error) {
		return s.duplicates(ctx, f, p)
	})
}

func (s *memoryAnalyticsService) UnknownSources(ctx context.Context, f MemoryAnalyticsFilter, p MemoryListParams) (*MemoryList[MemoryUnknownSource], error) {
	return memoryDetail(ctx, s, func(s *memoryAnalyticsService) (*MemoryList[MemoryUnknownSource], error) {
		return s.unknownSources(ctx, f, p)
	})
}
