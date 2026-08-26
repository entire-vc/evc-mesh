//go:build live

package teamrelay

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Live contract check for R8 against a real Team Relay.
//
// Build-tagged `live` so it never runs in CI: it needs a real agent key with
// the write scope and it writes to a real share. Run it deliberately:
//
//	MESH_TR_LIVE_RELAY_URL=https://cp.tr.entire.vc \
//	MESH_TR_LIVE_SHARE_ID=<share uuid> \
//	MESH_TR_LIVE_PATH=_probe/ee1745ce-sync-write-probe.md \
//	MESH_TR_LIVE_AGENT_KEY=<key> \
//	go test -tags live ./internal/integration/teamrelay/ -run TestLive -v
//
// It exists because every other test in this package proves the client against
// a double that I also wrote. That circularity is fine for the ordering logic
// and useless for the wire contract: if my idea of how the relay answers a
// failed precondition is wrong, my double is wrong in the same direction and
// every test agrees with itself. This one asks the actual server.
//
// It restores the document to the bytes it found, so it is re-runnable and
// leaves the share as it was.
func TestLiveSyncWrite_ConditionalWriteContract(t *testing.T) {
	relayURL := os.Getenv("MESH_TR_LIVE_RELAY_URL")
	shareID := os.Getenv("MESH_TR_LIVE_SHARE_ID")
	path := os.Getenv("MESH_TR_LIVE_PATH")
	agentKey := os.Getenv("MESH_TR_LIVE_AGENT_KEY")
	if relayURL == "" || shareID == "" || path == "" || agentKey == "" {
		t.Skip("live env not set")
	}
	ctx := context.Background()

	// Baseline: what the document is right now.
	before, err := SyncDownload(ctx, relayURL, shareID, path, agentKey)
	require.NoError(t, err, "baseline read must succeed or nothing below means anything")
	require.NotEmpty(t, before.SHA256, "the relay must give us a hash to use as a precondition")
	t.Logf("baseline: %d bytes, sha256=%s", len(before.Content), before.SHA256)

	// Always put it back, whatever happens below.
	t.Cleanup(func() {
		cur, cerr := SyncDownload(ctx, relayURL, shareID, path, agentKey)
		if cerr != nil {
			t.Errorf("cleanup: could not re-read %s: %v", path, cerr)
			return
		}
		if string(cur.Content) == string(before.Content) {
			return
		}
		if _, rerr := SyncWrite(ctx, relayURL, shareID, path, agentKey, cur.SHA256, before.Content); rerr != nil {
			t.Errorf("cleanup: could not restore %s: %v", path, rerr)
			return
		}
		t.Log("cleanup: restored to baseline bytes")
	})

	// --- AC-2, the negative control, run FIRST so it cannot be contaminated
	// by a write of ours that happens to have succeeded. ---
	wrongHash := "0000000000000000000000000000000000000000000000000000000000000000"
	_, err = SyncWrite(ctx, relayURL, shareID, path, agentKey, wrongHash, []byte("THIS MUST NEVER LAND\n"))
	require.Error(t, err, "a stale precondition must be refused")
	assert.ErrorIs(t, err, ErrSyncConflict, "and refused specifically as a conflict")

	afterRefusal, err := SyncDownload(ctx, relayURL, shareID, path, agentKey)
	require.NoError(t, err)
	assert.Equal(t, string(before.Content), string(afterRefusal.Content),
		"a refused write must leave the document byte-for-byte unchanged — checked by content, not by status code")
	assert.Equal(t, before.SHA256, afterRefusal.SHA256)

	// --- Unconditional writes are refused by the server too, not just by us. ---
	// The client refuses an empty precondition locally, so reach past it to ask
	// the server directly what it does with a wildcard. This is the assertion
	// that proves the server, and not merely our own guard, is what stands
	// between a bug and a blind overwrite.
	st, body := rawSyncWrite(t, relayURL, shareID, path, agentKey, "*", []byte("WILDCARD MUST NOT LAND\n"))
	assert.Equal(t, 428, st, "If-Match: * must be refused by the relay: %s", body)
	st, body = rawSyncWrite(t, relayURL, shareID, path, agentKey, "", []byte("NO PRECONDITION MUST NOT LAND\n"))
	assert.Equal(t, 428, st, "a write with no precondition must be refused by the relay: %s", body)

	afterUnconditional, err := SyncDownload(ctx, relayURL, shareID, path, agentKey)
	require.NoError(t, err)
	assert.Equal(t, string(before.Content), string(afterUnconditional.Content),
		"neither unconditional attempt may have changed the document")

	// --- AC-1: the accepted path. ---
	edited := append(append([]byte(nil), before.Content...), []byte("\n<!-- R8 live conditional-write probe -->\n")...)
	res, err := SyncWrite(ctx, relayURL, shareID, path, agentKey, before.SHA256, edited)
	require.NoError(t, err, "a write carrying the current hash must be accepted")
	require.NotEmpty(t, res.SHA256)
	assert.NotEqual(t, before.SHA256, res.SHA256, "an accepted write must produce a new version")

	readBack, err := SyncDownload(ctx, relayURL, shareID, path, agentKey)
	require.NoError(t, err)
	assert.Equal(t, string(edited), string(readBack.Content), "the edit must be readable back from the source")
	assert.Equal(t, res.SHA256, readBack.SHA256,
		"the hash the write returned must be the hash the next read reports — otherwise the next conditional write sends the wrong precondition")

	// --- AC-4: the old hash is now stale and must be refused. ---
	_, err = SyncWrite(ctx, relayURL, shareID, path, agentKey, before.SHA256, []byte("STALE REPLAY MUST NOT LAND\n"))
	assert.ErrorIs(t, err, ErrSyncConflict, "replaying the superseded hash must be refused, or the precondition means nothing")

	// And the rebuilt write, on the fresh hash, goes through.
	rebuilt := append(append([]byte(nil), readBack.Content...), []byte("<!-- rebuilt on the new original -->\n")...)
	_, err = SyncWrite(ctx, relayURL, shareID, path, agentKey, readBack.SHA256, rebuilt)
	require.NoError(t, err, "a write rebuilt on the current hash must be accepted")
}
