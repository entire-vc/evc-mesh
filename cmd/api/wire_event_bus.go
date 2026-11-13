package main

import (
	"context"
	"log"

	"github.com/entire-vc/evc-mesh/internal/eventbus"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/metrics"
)

// wireEventBus connects NATS and Redis for the event bus; the process
// continues without it when they are unavailable.
func wireEventBus(deps *apiDeps) {
	cfg := deps.cfg
	var err error
	workspaceRepo := deps.workspaceRepo
	projectRepo := deps.projectRepo
	eventBusRepo := deps.eventBusRepo
	eventBusService := deps.eventBusService
	memoryService := deps.memoryService
	webhookService := deps.webhookService

	// 6a. Connect to NATS and Redis for the event bus (graceful: continue without if unavailable).
	ebCfg := eventbus.EventBusConfig{
		NATSUrl:        cfg.NATS.URL,
		NATSMonitorURL: cfg.NATS.MonitorURL,
		RedisAddr:      cfg.Redis.Addr(),
		RedisPassword:  cfg.Redis.Password,
		RedisDB:        cfg.Redis.DB,
		NATSReplicas:   cfg.NATS.Replicas,
		StreamMaxAge:   cfg.NATS.StreamMaxAge,
		StreamMaxBytes: cfg.NATS.StreamMaxBytes,
		MaxMsgSize:     cfg.NATS.MaxMsgSize,
	}
	deps.eb, err = eventbus.New(context.Background(), ebCfg, eventBusRepo)
	if err != nil {
		// Loud degradation, not silent — mirrors the MESH_TRUSTED_PROXIES
		// pattern below: a control nobody can see failing gets trusted and
		// stops being checked. Surfaced three ways: this WARN, the /health
		// field below, and the mesh_event_bus_enabled gauge on /metrics.
		log.Printf("WARNING: Event bus unavailable, running without NATS/Redis: %v — "+
			"event publishing (WS broadcast, cross-agent notifications, event history) "+
			"is disabled for this process", err)
		deps.eb = nil
	} else {
		// Wire the event bus publisher into the event bus service.
		if configurable, ok := eventBusService.(service.EventBusServiceConfigurable); ok {
			configurable.SetEventBus(deps.eb, workspaceRepo, projectRepo)
		}
		// Start background workers (PG writer + cleanup).
		deps.eb.Start()
		// delivery=nats subscriptions of extensions publish through the same bus.
		if n, ok := webhookService.(service.ExtensionNATSConfigurable); ok {
			n.SetExtensionNATS(deps.eb, workspaceRepo)
		}
	}
	metrics.SetEventBusEnabled(deps.eb != nil)
	// Wire memory service into eventBusService for memory extraction on Publish().
	// Done outside the NATS block so memory extraction works even without NATS.
	if configurable, ok := eventBusService.(service.EventBusServiceConfigurable); ok {
		configurable.SetMemoryService(memoryService)
	}
}
