package service

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

func TestNewChatRetentionJob_DefaultsInterval(t *testing.T) {
	require.Equal(t, time.Hour, NewChatRetentionJob(nil, 0).interval)
	require.Equal(t, time.Minute, NewChatRetentionJob(nil, time.Minute).interval)
}

func TestChatRetentionJob_RunOnceAndRunStop(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to run the chat retention job against PostgreSQL")
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	job := NewChatRetentionJob(postgres.NewChatThreadDeleteRepo(db, nil), time.Millisecond)

	job.RunOnce(context.Background()) // nothing eligible: must not panic or error out

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { job.Run(ctx); close(done) }()
	time.Sleep(20 * time.Millisecond) // a few ticks
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after its context was cancelled")
	}
}
