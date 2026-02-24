package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/pressly/goose/v3"

	"github.com/entire-vc/evc-mesh/internal/config"
	"github.com/entire-vc/evc-mesh/internal/eventbus"
	mcpserver "github.com/entire-vc/evc-mesh/internal/mcp"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/internal/storage"

	sdkserver "github.com/mark3labs/mcp-go/server"
)

func main() {
	// All logging goes to stderr so that stdout is reserved for MCP JSON-RPC.
	log.SetOutput(os.Stderr)

	// Parse CLI flags.
	transportFlag := flag.String("transport", "", "Transport mode: stdio or sse (overrides MESH_MCP_TRANSPORT)")
	flag.Parse()

	// 1. Determine transport mode from flag or env var.
	transport := "stdio"
	if envTransport := os.Getenv("MESH_MCP_TRANSPORT"); envTransport != "" {
		transport = strings.ToLower(envTransport)
	}
	if *transportFlag != "" {
		transport = strings.ToLower(*transportFlag)
	}
	if transport != "stdio" && transport != "sse" {
		log.Fatalf("Invalid transport %q: must be 'stdio' or 'sse'", transport)
	}

	// 2. For stdio mode, require MESH_AGENT_KEY upfront.
	//    For SSE mode, agent keys are provided per-connection via HTTP headers/query params.
	agentKey := os.Getenv("MESH_AGENT_KEY")
	if transport == "stdio" && agentKey == "" {
		log.Fatal("MESH_AGENT_KEY environment variable is required for stdio mode")
	}

	// 3. Load configuration.
	cfg := config.Load()

	// Override database DSN from MESH_DATABASE_URL if provided.
	if dsn := os.Getenv("MESH_DATABASE_URL"); dsn != "" {
		// For MESH_DATABASE_URL we expect a full connection string; override the
		// Config.Database so that cfg.Database.DSN() returns it.
		cfg.Database.Host = ""
		cfg.Database.Port = 0
		cfg.Database.User = ""
		cfg.Database.Password = ""
		cfg.Database.Name = ""
		cfg.Database.SSLMode = ""
		// We will use dsn directly below.
		_ = dsn
	}

	// Determine the DSN to use.
	dsn := cfg.Database.DSN()
	if envDSN := os.Getenv("MESH_DATABASE_URL"); envDSN != "" {
		dsn = envDSN
	}

	// 4. Connect to PostgreSQL.
	db, err := postgres.NewDB(dsn)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()
	log.Println("Connected to PostgreSQL")

	// 5. Run database migrations.
	if err := goose.Up(db.DB, "migrations"); err != nil {
		// Non-fatal for MCP: migrations might already be applied by the API server.
		log.Printf("Warning: migration failed (may already be applied): %v", err)
	}

	// 6. Create all repository instances.
	workspaceRepo := postgres.NewWorkspaceRepo(db)
	projectRepo := postgres.NewProjectRepo(db)
	taskRepo := postgres.NewTaskRepo(db)
	taskStatusRepo := postgres.NewTaskStatusRepo(db)
	taskDependencyRepo := postgres.NewTaskDependencyRepo(db)
	commentRepo := postgres.NewCommentRepo(db)
	artifactRepo := postgres.NewArtifactRepo(db)
	agentRepo := postgres.NewAgentRepo(db)
	eventBusRepo := postgres.NewEventBusMessageRepo(db)
	activityLogRepo := postgres.NewActivityLogRepo(db)
	customFieldRepo := postgres.NewCustomFieldDefinitionRepo(db)

	// 7. Create all service instances.
	workspaceService := service.NewWorkspaceService(workspaceRepo, activityLogRepo)
	projectService := service.NewProjectService(projectRepo, taskStatusRepo, activityLogRepo)
	taskService := service.NewTaskService(taskRepo, taskStatusRepo, taskDependencyRepo, activityLogRepo)
	taskStatusService := service.NewTaskStatusService(taskStatusRepo, taskRepo, activityLogRepo)
	taskDependencyService := service.NewTaskDependencyService(taskDependencyRepo, taskRepo, activityLogRepo)
	commentService := service.NewCommentService(commentRepo, taskRepo, activityLogRepo)
	agentService := service.NewAgentService(agentRepo, activityLogRepo, workspaceRepo)
	eventBusService := service.NewEventBusService(eventBusRepo, activityLogRepo)
	activityLogService := service.NewActivityLogService(activityLogRepo)
	customFieldService := service.NewCustomFieldService(customFieldRepo, activityLogRepo)

	// Connect to NATS and Redis for the event bus (graceful: continue without if unavailable).
	var eb *eventbus.EventBus
	ebCfg := eventbus.EventBusConfig{
		NATSUrl:       cfg.NATS.URL,
		RedisAddr:     cfg.Redis.Addr(),
		RedisPassword: cfg.Redis.Password,
		RedisDB:       cfg.Redis.DB,
	}
	eb, ebErr := eventbus.New(context.Background(), ebCfg, eventBusRepo)
	if ebErr != nil {
		log.Printf("Warning: Event bus unavailable, running without NATS/Redis: %v", ebErr)
		eb = nil
	} else {
		// Wire the event bus publisher into the event bus service.
		if configurable, ok := eventBusService.(service.EventBusServiceConfigurable); ok {
			configurable.SetEventBus(eb, workspaceRepo, projectRepo)
		}
		// Start background workers.
		eb.Start()
		defer func() {
			if err := eb.Close(); err != nil {
				log.Printf("Error closing event bus: %v", err)
			}
		}()
	}

	// Create artifact service with S3 storage.
	var artifactService service.ArtifactService
	s3Client, err := storage.NewS3Client(
		cfg.S3.Endpoint,
		cfg.S3.AccessKeyID,
		cfg.S3.SecretAccessKey,
		cfg.S3.Bucket,
		cfg.S3.Region,
		cfg.S3.UseSSL,
	)
	if err != nil {
		log.Printf("Warning: S3 storage unavailable, using stub artifact service: %v", err)
		artifactService = newStubArtifactService(artifactRepo)
	} else {
		artifactService = service.NewArtifactService(artifactRepo, s3Client, activityLogRepo)
	}

	// 8. Build common server config (shared between stdio and SSE).
	serverCfg := mcpserver.ServerConfig{
		WorkspaceService:      workspaceService,
		ProjectService:        projectService,
		TaskService:           taskService,
		TaskStatusService:     taskStatusService,
		TaskDependencyService: taskDependencyService,
		CommentService:        commentService,
		ArtifactService:       artifactService,
		AgentService:          agentService,
		EventBusService:       eventBusService,
		ActivityLogService:    activityLogService,
		CustomFieldService:    customFieldService,
	}

	// 9. Start transport.
	switch transport {
	case "stdio":
		// Authenticate the agent once at startup for stdio mode.
		workspaceSlug, err := extractWorkspaceSlug(agentKey)
		if err != nil {
			log.Fatalf("Invalid MESH_AGENT_KEY format: %v", err)
		}

		log.Printf("Authenticating agent (workspace: %s)...", workspaceSlug)
		agent, err := agentService.Authenticate(context.Background(), workspaceSlug, agentKey)
		if err != nil {
			log.Fatalf("Agent authentication failed: %v", err)
		}
		log.Printf("Authenticated as agent: %s (ID: %s, type: %s)", agent.Name, agent.ID, agent.AgentType)

		serverCfg.Session = &mcpserver.AgentSession{
			AgentID:     agent.ID,
			WorkspaceID: agent.WorkspaceID,
			AgentName:   agent.Name,
			AgentType:   string(agent.AgentType),
		}

		srv := mcpserver.NewServer(serverCfg)
		log.Println("Starting MCP server on stdio transport...")
		if err := sdkserver.ServeStdio(srv.MCPServer()); err != nil {
			log.Fatalf("MCP server error: %v", err)
		}

	case "sse":
		// SSE mode: per-connection authentication via HTTP headers/query params.
		// Session is nil; each request authenticates via WithSSEContextFunc.
		srv := mcpserver.NewServer(serverCfg)

		// Create a per-connection session cache keyed by agent key.
		// This avoids re-authenticating on every JSON-RPC message.
		sessionCache := &agentSessionCache{
			agentService: agentService,
		}

		host := os.Getenv("MESH_MCP_HOST")
		if host == "" {
			host = "0.0.0.0"
		}
		port := os.Getenv("MESH_MCP_PORT")
		if port == "" {
			port = "8081"
		}
		addr := host + ":" + port
		baseURL := fmt.Sprintf("http://%s:%s", host, port)

		sseServer := sdkserver.NewSSEServer(
			srv.MCPServer(),
			sdkserver.WithBaseURL(baseURL),
			sdkserver.WithKeepAlive(true),
			// Inject the authenticated agent session into the context for each
			// JSON-RPC message request.
			sdkserver.WithSSEContextFunc(func(ctx context.Context, r *http.Request) context.Context {
				agentKey := extractAgentKeyFromRequest(r)
				if agentKey == "" {
					log.Printf("SSE request without agent key from %s", r.RemoteAddr)
					return ctx
				}

				session, err := sessionCache.GetOrAuthenticate(ctx, agentKey)
				if err != nil {
					log.Printf("SSE auth failed for key %s...: %v", safeKeyPrefix(agentKey), err)
					return ctx
				}

				return mcpserver.ContextWithSession(ctx, session)
			}),
		)

		// Wrap the SSE endpoint handler to validate agent key at connection time.
		mux := http.NewServeMux()
		mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
			agentKey := extractAgentKeyFromRequest(r)
			if agentKey == "" {
				http.Error(w, "Missing agent key: provide Authorization: Bearer agk_..., X-Agent-Key header, or ?agent_key query param", http.StatusUnauthorized)
				return
			}

			// Validate the key at connection time to fail fast.
			_, err := sessionCache.GetOrAuthenticate(r.Context(), agentKey)
			if err != nil {
				log.Printf("SSE connection auth failed for key %s...: %v", safeKeyPrefix(agentKey), err)
				http.Error(w, fmt.Sprintf("Authentication failed: %v", err), http.StatusForbidden)
				return
			}

			// Proxy to the real SSE handler.
			sseServer.SSEHandler().ServeHTTP(w, r)
		})
		mux.Handle("/message", sseServer.MessageHandler())

		log.Printf("Starting MCP SSE server on %s (multi-agent mode)", addr)
		log.Printf("  SSE endpoint:     %s/sse", baseURL)
		log.Printf("  Message endpoint: %s/message", baseURL)
		log.Printf("  Auth: Authorization: Bearer agk_..., X-Agent-Key, or ?agent_key=agk_...")

		httpServer := &http.Server{
			Addr:    addr,
			Handler: mux,
		}
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("MCP SSE server error: %v", err)
		}
	}
}

// extractWorkspaceSlug parses the workspace slug from an API key.
// Key format: agk_{workspace_slug}_{random_hex}
func extractWorkspaceSlug(key string) (string, error) {
	if !strings.HasPrefix(key, "agk_") {
		return "", fmt.Errorf("key must start with 'agk_'")
	}

	// Remove the "agk_" prefix.
	rest := key[4:]

	// Find the last underscore to split slug from random part.
	lastUnderscore := strings.LastIndex(rest, "_")
	if lastUnderscore <= 0 {
		return "", fmt.Errorf("key must have format 'agk_{slug}_{random}'")
	}

	slug := rest[:lastUnderscore]
	if slug == "" {
		return "", fmt.Errorf("workspace slug is empty")
	}

	return slug, nil
}

// extractAgentKeyFromRequest extracts the agent API key from an HTTP request.
// It checks (in order): Authorization Bearer header, X-Agent-Key header, agent_key query param.
func extractAgentKeyFromRequest(r *http.Request) string {
	// 1. Authorization: Bearer agk_...
	if auth := r.Header.Get("Authorization"); auth != "" {
		const bearerPrefix = "Bearer "
		if strings.HasPrefix(auth, bearerPrefix) {
			token := strings.TrimSpace(auth[len(bearerPrefix):])
			if strings.HasPrefix(token, "agk_") {
				return token
			}
		}
	}

	// 2. X-Agent-Key: agk_...
	if key := r.Header.Get("X-Agent-Key"); key != "" && strings.HasPrefix(key, "agk_") {
		return key
	}

	// 3. ?agent_key=agk_...
	if key := r.URL.Query().Get("agent_key"); key != "" && strings.HasPrefix(key, "agk_") {
		return key
	}

	return ""
}

// safeKeyPrefix returns a safe prefix of the key for logging (avoids leaking full key).
func safeKeyPrefix(key string) string {
	if len(key) > 12 {
		return key[:12]
	}
	return key
}

// agentSessionCache provides thread-safe caching of authenticated agent sessions
// keyed by their API key. This avoids calling Authenticate on every JSON-RPC message.
type agentSessionCache struct {
	mu           sync.RWMutex
	cache        map[string]*mcpserver.AgentSession
	agentService service.AgentService
}

// GetOrAuthenticate returns a cached session or authenticates the agent and caches the result.
func (c *agentSessionCache) GetOrAuthenticate(ctx context.Context, agentKey string) (*mcpserver.AgentSession, error) {
	// Fast path: check cache with read lock.
	c.mu.RLock()
	if c.cache != nil {
		if session, ok := c.cache[agentKey]; ok {
			c.mu.RUnlock()
			return session, nil
		}
	}
	c.mu.RUnlock()

	// Slow path: authenticate and cache.
	workspaceSlug, err := extractWorkspaceSlug(agentKey)
	if err != nil {
		return nil, fmt.Errorf("invalid agent key format: %w", err)
	}

	agent, err := c.agentService.Authenticate(ctx, workspaceSlug, agentKey)
	if err != nil {
		return nil, fmt.Errorf("authentication failed: %w", err)
	}

	session := &mcpserver.AgentSession{
		AgentID:     agent.ID,
		WorkspaceID: agent.WorkspaceID,
		AgentName:   agent.Name,
		AgentType:   string(agent.AgentType),
	}

	c.mu.Lock()
	if c.cache == nil {
		c.cache = make(map[string]*mcpserver.AgentSession)
	}
	c.cache[agentKey] = session
	c.mu.Unlock()

	log.Printf("SSE: authenticated agent %s (ID: %s, workspace: %s)", agent.Name, agent.ID, workspaceSlug)
	return session, nil
}
