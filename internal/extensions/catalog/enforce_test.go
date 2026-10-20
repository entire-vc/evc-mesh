package catalog

import (
	"context"
	"testing"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

type memStore struct {
	rows map[string]*domain.InstalledExtension
}

func (m *memStore) List(context.Context) ([]domain.InstalledExtension, error) {
	var out []domain.InstalledExtension
	for _, r := range m.rows {
		out = append(out, *r)
	}
	return out, nil
}

func (m *memStore) SetBlocked(_ context.Context, id string, blocked bool, reason string) (bool, error) {
	r := m.rows[id]
	if r.Source == domain.ExtensionSourceBuiltin || (r.Blocked == blocked && r.BlockedReason == reason) {
		return false, nil
	}
	r.Blocked, r.BlockedReason = blocked, reason
	return true, nil
}

type memJournal struct{ actions []string }

func (j *memJournal) AppendJournal(_ context.Context, e *domain.InstanceExtensionJournalEntry) error {
	j.actions = append(j.actions, e.ExtensionID+":"+e.Action)
	return nil
}

func TestEnforcer_BlockAndRestore(t *testing.T) {
	st := &memStore{rows: map[string]*domain.InstalledExtension{
		"hello":    {ExtensionID: "hello", Version: "1.0.0", Source: domain.ExtensionSourceCatalog},
		"other":    {ExtensionID: "other", Version: "1.0.0", Source: domain.ExtensionSourceRepo},
		"telegram": {ExtensionID: "telegram", Version: "1.0.0", Source: domain.ExtensionSourceBuiltin},
	}}
	j := &memJournal{}
	var notified, resyncs int
	e := &Enforcer{Store: st, Journal: j,
		Notify:   func(context.Context, string, string, bool) { notified++ },
		OnChange: func(context.Context) { resyncs++ }}
	ctx := context.Background()

	cat := &Catalog{Blocklist: []Block{{ID: "hello", Reason: "malware"}, {ID: "telegram", Reason: "never"}}}
	e.Apply(ctx, cat)
	if !st.rows["hello"].Blocked || st.rows["hello"].BlockedReason != "malware" {
		t.Fatal("hello must be blocked with the reason")
	}
	if st.rows["other"].Blocked || st.rows["telegram"].Blocked {
		t.Fatal("unlisted and builtin rows must stay on")
	}
	e.Apply(ctx, cat)
	if notified != 1 || resyncs != 1 || len(j.actions) != 1 {
		t.Fatalf("second pass must be quiet: notified=%d resyncs=%d journal=%v", notified, resyncs, j.actions)
	}
	e.Apply(ctx, &Catalog{})
	if st.rows["hello"].Blocked {
		t.Fatal("removed from the blocklist, must be back on")
	}
	if len(j.actions) != 2 || j.actions[1] != "hello:"+InstanceJournalUnblocked {
		t.Fatalf("journal: %v", j.actions)
	}
}

func TestEnforcer_VersionScopedBlock(t *testing.T) {
	st := &memStore{rows: map[string]*domain.InstalledExtension{
		"hello": {ExtensionID: "hello", Version: "1.1.0", Source: domain.ExtensionSourceCatalog},
	}}
	e := &Enforcer{Store: st}
	e.Apply(context.Background(), &Catalog{Blocklist: []Block{{ID: "hello", Version: "1.0.0", Reason: "old"}}})
	if st.rows["hello"].Blocked {
		t.Fatal("a block on 1.0.0 must not switch off 1.1.0")
	}
}
