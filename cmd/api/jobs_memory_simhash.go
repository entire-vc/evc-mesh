package main

import (
	"context"
	"log"
	"time"

	"github.com/entire-vc/evc-mesh/internal/reconciler"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/internal/simhash"
)

// memorySimhashBackfillBatch is the rows per transaction of the recompute.
const memorySimhashBackfillBatch = 500

// runMemorySimhashBackfill recomputes content_simhash of the stored memories
// with the current simhash.Algorithm once, then brings the daily aggregates
// counted on the old hashes onto the new ones: the
// memory_base_daily dup columns are recounted, the memory_stats_daily days
// with misses are queued for the nightly job's rewrite. Both steps record their
// completion in memory_simhash_backfill, so a later start does nothing; a run
// interrupted before completion is redone from the first row on the next start
// (rows already current are only read). A row skipped because its content
// changed meanwhile got the new hash from that write (Upsert computes it). A
// replica that finds the recompute locked leaves both steps to the holder.
//
// Last, the linker supersedes decided on the old hashes are repaired once on
// the new ones (RepairLinkerSimhashSupersedes): it needs the
// finished recompute, so it runs after it, and a failed recount does not hold
// it back.
func runMemorySimhashBackfill(ctx context.Context, memoryRepo *postgres.MemoryRepo, rollupRepo *postgres.MemoryDailyRollupRepo) {
	res, err := memoryRepo.BackfillContentSimhash(ctx, simhash.Algorithm, simhash.Compute, memorySimhashBackfillBatch)
	if err != nil {
		log.Printf("[memory-simhash] ERROR: recompute %s: %v (scanned %d, updated %d, skipped %d)",
			simhash.Algorithm, err, res.Scanned, res.Updated, res.Skipped)
		return
	}
	if res.Locked {
		log.Printf("[memory-simhash] recompute %s runs elsewhere", simhash.Algorithm)
		return
	}
	if !res.AlreadyDone {
		log.Printf("[memory-simhash] recomputed %s: scanned %d, updated %d, skipped (content changed) %d",
			simhash.Algorithm, res.Scanned, res.Updated, res.Skipped)
	}
	statsFrom := time.Now().Add(-service.MemoryRollupLookback)
	baseDays, statsDays, err := rollupRepo.RecountSimhashAggregates(ctx, simhash.Algorithm, statsFrom)
	switch {
	case err != nil:
		log.Printf("[memory-simhash] ERROR: recount daily aggregates: %v", err)
	case baseDays > 0 || statsDays > 0:
		log.Printf("[memory-simhash] recounted memory_base_daily dup columns of %d day(s); %d memory_stats_daily day(s) queued for rewrite",
			baseDays, statsDays)
	}

	rep, err := memoryRepo.RepairLinkerSimhashSupersedes(ctx, simhash.Algorithm, reconciler.LinkerSimhashMaxBits)
	switch {
	case err != nil:
		log.Printf("[memory-simhash] ERROR: repair linker supersedes: %v", err)
	case rep.NotReady:
		log.Printf("[memory-simhash] linker supersede repair waits for the %s recompute", simhash.Algorithm)
	case !rep.AlreadyDone:
		log.Printf("[memory-simhash] linker supersedes on the old hash: %d back to review_needed, %d re-pointed to the current row of their key, journal memory_linker_simhash_repair_log",
			rep.Restored, rep.Repointed)
	}
}
