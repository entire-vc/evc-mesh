package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"

	"github.com/entire-vc/evc-mesh/internal/config"
	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// EnvSource describes how one extension's instance-level setting was
// configured through MESH_* env before instance_extension_settings existed.
type EnvSource struct {
	ExtensionID string
	// SecretKeys are the settings fields stored encrypted (manifest x-secret).
	SecretKeys []string
	// Read returns the settings and enabled flag the env describes, or
	// ok=false when the env does not configure this extension at all.
	Read func(cfg *config.Config) (settings map[string]any, enabled bool, ok bool)
}

// BuiltinEnvSources lists the extensions that have env-based instance config.
func BuiltinEnvSources() []EnvSource {
	return []EnvSource{
		{
			ExtensionID: string(domain.IntegrationProviderGitLab),
			SecretKeys:  []string{"token", "webhook_secret"},
			Read: func(c *config.Config) (map[string]any, bool, bool) {
				w := c.Webhook
				if w.GitLabURL == "" || w.GitLabToken == "" {
					return nil, false, false
				}
				return map[string]any{"base_url": w.GitLabURL, "token": w.GitLabToken, "webhook_secret": w.GitLabSecret}, true, true
			},
		},
		{
			ExtensionID: string(domain.IntegrationProviderGitHub),
			SecretKeys:  []string{"token", "webhook_secret"},
			Read: func(c *config.Config) (map[string]any, bool, bool) {
				w := c.Webhook
				if w.GitHubToken == "" && w.GitHubSecret == "" {
					return nil, false, false
				}
				return map[string]any{"token": w.GitHubToken, "webhook_secret": w.GitHubSecret}, true, true
			},
		},
		{
			ExtensionID: string(domain.IntegrationProviderSpark),
			Read: func(c *config.Config) (map[string]any, bool, bool) {
				if c.Spark.URL == "" {
					return nil, false, false
				}
				return map[string]any{"spark_url": c.Spark.URL}, c.Spark.Enabled, true
			},
		},
		{
			ExtensionID: string(domain.IntegrationProviderTeamRelay),
			Read: func(c *config.Config) (map[string]any, bool, bool) {
				if c.TeamRelay.RelayURL == "" {
					return nil, false, false
				}
				// The push transport was only ever switched by this variable,
				// read straight from the environment by the relay client.
				return map[string]any{
					"relay_url":         c.TeamRelay.RelayURL,
					"transport_enabled": os.Getenv("MESH_TEAMRELAY_TRANSPORT_ENABLED") == "true",
				}, true, true
			},
		},
		{
			ExtensionID: string(domain.IntegrationProviderTelegram),
			Read: func(c *config.Config) (map[string]any, bool, bool) {
				// The config default is "on"; only an explicit variable counts
				// as the operator having configured the instance.
				if _, set := os.LookupEnv("MESH_TELEGRAM_ENABLED"); !set {
					return nil, false, false
				}
				return map[string]any{}, c.Telegram.Enabled, true
			},
		},
	}
}

// BootstrapInstanceSettings copies env-configured extensions into
// instance_extension_settings with source=env. Idempotent: an existing row
// (env- or admin-written) is never touched. It also logs every extension that
// ends up with neither a row nor env, so "provider is off" is visible at start.
func BootstrapInstanceSettings(ctx context.Context, repo repository.InstanceExtensionSettingRepository, cfg *config.Config, sources []EnvSource) error {
	for _, src := range sources {
		settings, enabled, ok := src.Read(cfg)
		if !ok {
			row, err := repo.Get(ctx, src.ExtensionID)
			if err != nil {
				return fmt.Errorf("instance settings %q: %w", src.ExtensionID, err)
			}
			if row == nil {
				log.Printf("[extensions] %s: no instance setting and no MESH_* env — disabled at instance level", src.ExtensionID)
			}
			continue
		}
		blob, err := json.Marshal(settings)
		if err != nil {
			return err
		}
		inserted, err := repo.InsertIfAbsent(ctx, &domain.InstanceExtensionSetting{
			ExtensionID: src.ExtensionID,
			Settings:    blob,
			Enabled:     enabled,
			Source:      domain.InstanceSettingSourceEnv,
		}, src.SecretKeys)
		if err != nil {
			return fmt.Errorf("instance settings %q: %w", src.ExtensionID, err)
		}
		if inserted {
			log.Printf("[extensions] %s: instance setting created from MESH_* env (source=env)", src.ExtensionID)
		}
	}
	return nil
}

// DBInstanceStore is the InstanceStore backed by instance_extension_settings.
// For one release an extension with no row but with env still resolves from
// env, with a warning; the fallback is removed in the following release.
type DBInstanceStore struct {
	Repo    repository.InstanceExtensionSettingRepository
	Cfg     *config.Config
	Sources []EnvSource

	warned sync.Map
}

func (s *DBInstanceStore) InstanceSetting(ctx context.Context, extensionID string) (*InstanceSetting, error) {
	row, err := s.Repo.Get(ctx, extensionID)
	if err != nil {
		return nil, err
	}
	if row != nil {
		settings := map[string]any{}
		if len(row.Settings) > 0 {
			if err := json.Unmarshal(row.Settings, &settings); err != nil {
				return nil, fmt.Errorf("instance setting %q: bad settings: %w", extensionID, err)
			}
		}
		return &InstanceSetting{Settings: settings, Enabled: row.Enabled}, nil
	}
	for _, src := range s.Sources {
		if src.ExtensionID != extensionID || s.Cfg == nil {
			continue
		}
		settings, enabled, ok := src.Read(s.Cfg)
		if !ok {
			return nil, nil
		}
		if _, dup := s.warned.LoadOrStore(extensionID, true); !dup {
			log.Printf("[extensions] WARNING: настройка %s взята из env, перенесите в настройки инстанса", extensionID)
		}
		return &InstanceSetting{Settings: settings, Enabled: enabled}, nil
	}
	return nil, nil
}
