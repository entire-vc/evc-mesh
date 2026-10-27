package extensions

import (
	"context"

	"github.com/google/uuid"
)

// Connection is a workspace-level connection row for one extension.
type Connection struct {
	Settings map[string]any
	IsActive bool
}

// InstanceSetting is the instance-level setting for one extension.
type InstanceSetting struct {
	Settings map[string]any
	Enabled  bool
}

// ConnectionStore reads workspace connections. A missing row is (nil, nil).
type ConnectionStore interface {
	Connection(ctx context.Context, workspaceID uuid.UUID, extensionID string) (*Connection, error)
}

// InstanceStore reads instance-level settings. A missing entry is (nil, nil).
type InstanceStore interface {
	InstanceSetting(ctx context.Context, extensionID string) (*InstanceSetting, error)
}

// ResolveSettings is the single implementation of the resolution order
// (spec 03 §1): workspace connection -> instance setting -> off.
//
// Levels never mix. A workspace row governs whenever it exists: is_active=false
// means "off in this workspace" and does not fall through to the instance
// setting (and does not touch it). Only the absence of a row lets the
// instance level decide. inst may be nil (no instance level yet).
//
// The allow-list governs over both levels: a refused extension resolves as
// off with NotAllowed set. allow may be nil (everything allowed).
func ResolveSettings(ctx context.Context, conns ConnectionStore, inst InstanceStore, allow AllowList, workspaceID uuid.UUID, extensionID string) (Resolved, error) {
	res, err := resolveLevels(ctx, conns, inst, workspaceID, extensionID)
	if err != nil {
		return Resolved{}, err
	}
	return ApplyAllowList(ctx, allow, extensionID, res)
}

func resolveLevels(ctx context.Context, conns ConnectionStore, inst InstanceStore, workspaceID uuid.UUID, extensionID string) (Resolved, error) {
	res := Resolved{ExtensionID: extensionID, Source: SourceNone}

	if conns != nil {
		c, err := conns.Connection(ctx, workspaceID, extensionID)
		if err != nil {
			return Resolved{}, err
		}
		if c != nil {
			res.Source = SourceWorkspace
			res.Enabled = c.IsActive
			if c.IsActive {
				res.Settings = c.Settings
			}
			return res, nil
		}
	}

	if inst != nil {
		s, err := inst.InstanceSetting(ctx, extensionID)
		if err != nil {
			return Resolved{}, err
		}
		if s != nil && s.Enabled {
			res.Source = SourceInstance
			res.Enabled = true
			res.Settings = s.Settings
		}
	}
	return res, nil
}

// BaseProvider gives an extension the shared Resolve plus neutral defaults for
// Health, Hooks and UI. Embed it and override what differs; Validate stays
// with the extension.
type BaseProvider struct {
	Man   Manifest
	Conns ConnectionStore
	Inst  InstanceStore
	Allow AllowList
}

func (b BaseProvider) Manifest() Manifest { return b.Man }

func (b BaseProvider) Resolve(ctx context.Context, workspaceID uuid.UUID) (Resolved, error) {
	return ResolveSettings(ctx, b.Conns, b.Inst, b.Allow, workspaceID, b.Man.ID)
}

func (b BaseProvider) Health(_ context.Context, r Resolved) HealthReport {
	return HealthReport{OK: r.Enabled}
}

func (b BaseProvider) Hooks() Hooks { return Hooks{} }

func (b BaseProvider) UI() UIContribs { return UIContribs{} }
