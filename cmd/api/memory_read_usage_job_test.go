package main

import (
	"testing"

	"github.com/jmoiron/sqlx"
)

// MEMORY_READ_LOG=false switches off the journal and the usage job together.
func TestNewMemoryReadUsageJob_FollowsMemoryReadLogFlag(t *testing.T) {
	if job := newMemoryReadUsageJob(false, &sqlx.DB{}); job != nil {
		t.Fatalf("flag off: want no usage job, got %v", job)
	}
	if job := newMemoryReadUsageJob(true, &sqlx.DB{}); job == nil {
		t.Fatal("flag on: want the usage job")
	}
}
