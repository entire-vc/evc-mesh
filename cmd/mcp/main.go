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
	"time"

	"github.com/entire-vc/evc-mesh/internal/config"
	mcpserver "github.com/entire-vc/evc-mesh/internal/mcp"
	"github.com/entire-vc/evc-mesh/internal/middleware"

	sdkserver "github.com/mark3labs/mcp-go/server"
	"github.com/prometheus/client_golang/prometheus/promhttp"
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

	// 2. Get REST API base URL.
	apiURL := os.Getenv("MESH_API_URL")
	if apiURL == "" {
		apiURL = "http://localhost:8005"
	}

	// 3. For stdio mode, require MESH_AGENT_KEY upfront.
	//    For SSE mode, agent keys are provided per-connection via HTTP headers/query params.
	agentKey := os.Getenv("MESH_AGENT_KEY")
	if transport == "stdio" && agentKey == "" {
		log.Fatal("MESH_AGENT_KEY environment variable is required for stdio mode")
	}

	// 4. Start transport.
	switch transport {
	case "stdio":
		restClient := mcpserver.NewRESTClient(apiURL, agentKey)

		// Verify connectivity and get agent info.
		log.Printf("Connecting to Mesh API at %s...", apiURL)
		agentInfo, err := restClient.GetAgentMe(context.Background())
		if err != nil {
			log.Fatalf("Agent authentication failed: %v", err)
		}

		agentID, _ := agentInfo["id"].(string)
		agentName, _ := agentInfo["name"].(string)
		agentType, _ := agentInfo["agent_type"].(string)
		workspaceID, _ := agentInfo["workspace_id"].(string)

		log.Printf("Authenticated as agent: %s (ID: %s, type: %s)", agentName, agentID, agentType)

		// Parse UUIDs.
		session, err := buildSession(agentID, workspaceID, agentName, agentType)
		if err != nil {
			log.Fatalf("Invalid agent data from API: %v", err)
		}

		// Read profile from env var; default to full.
		profile := os.Getenv("MESH_MCP_PROFILE")
		if profile == "" {
			profile = mcpserver.ProfileFull
		}

		cfg := mcpserver.ServerConfig{
			Session:    session,
			RESTClient: restClient,
			Profile:    profile,
		}

		srv := mcpserver.NewServer(cfg)
		log.Printf("Starting MCP server on stdio transport (profile: %s)...", profile)
		if err := sdkserver.ServeStdio(srv.MCPServer()); err != nil {
			log.Fatalf("MCP server error: %v", err)
		}

	case "sse":
		// SSE mode: per-connection authentication via HTTP headers/query params.
		// Create session cache that authenticates via REST API.
		sessionCache := &agentSessionCache{
			apiURL: apiURL,
		}

		// For SSE mode, create a server without a static session.
		// Per-connection sessions are injected via the SSE context function.
		// The server's RESTClient will be overridden per-connection via context,
		// so we create a placeholder server — the actual REST client is per-connection.
		//
		// Since the mcp-go Server holds the RESTClient, we create a single server
		// that reads the session from context. The RESTClient in the server is
		// unused for SSE mode — handlers use the agent key from the session context
		// combined with the configured API URL.
		//
		// For SSE multi-agent: each connection's agent key is authenticated once,
		// and the session (including agent ID and workspace) is stored in context.
		// The shared RESTClient uses no default agent key (will be set per-request
		// via context-level agent key injection).
		//
		// Implementation note: the shared RESTClient will not work for multi-agent
		// SSE since it has a single agent key. Instead, we cache a RESTClient per
		// agent key and inject it into context via ContextWithRESTClient.
		//
		// We create the base server with an empty agent key; per-connection REST
		// clients are stored in the session cache and accessed via context.

		// We need a server with per-session REST clients for SSE mode.
		// Use a server registry: map agentKey -> *Server.
		srvRegistry := &serverRegistry{
			apiURL: apiURL,
		}

		// Start TTL-based eviction goroutines: run every 5 minutes, evict
		// entries idle for 30 minutes. Mirrors the rateLimitStore pattern.
		sessionCache.startCleanup(5*time.Minute, 30*time.Minute)
		srvRegistry.startCleanup(5*time.Minute, 30*time.Minute)

		host := os.Getenv("MESH_MCP_HOST")
		if host == "" {
			host = "0.0.0.0"
		}
		port := os.Getenv("MESH_MCP_PORT")
		if port == "" {
			port = "8081"
		}
		addr := host + ":" + port
		publicURL := strings.TrimSpace(os.Getenv("MESH_MCP_PUBLIC_URL"))

		// Build the shared SSE context function used by both core and full servers.
		// It injects the authenticated agent session and per-agent REST client.
		sseContextFunc := func(ctx context.Context, r *http.Request) context.Context {
			key := extractAgentKeyFromRequest(r)
			if key == "" {
				log.Printf("SSE request without agent key from %s", r.RemoteAddr)
				return ctx
			}

			session, err := sessionCache.GetOrAuthenticate(ctx, key)
			if err != nil {
				log.Printf("SSE auth failed for key %s...: %v", safeKeyPrefix(key), err)
				return ctx
			}

			// Inject per-agent REST client and session into context.
			perAgentClient := srvRegistry.GetClient(key)
			ctx = mcpserver.ContextWithSession(ctx, session)
			ctx = mcpserver.ContextWithRESTClient(ctx, perAgentClient)
			return ctx
		}

		// Create shared REST client (unused directly; per-agent clients injected via context).
		sharedRestClient := mcpserver.NewRESTClient(apiURL, "")

		// Create the full-profile server (default, backward-compatible).
		fullSrv := mcpserver.NewServer(mcpserver.ServerConfig{
			RESTClient: sharedRestClient,
			Profile:    mcpserver.ProfileFull,
		})

		// Create the core-profile server (lightweight endpoint).
		coreSrv := mcpserver.NewServer(mcpserver.ServerConfig{
			RESTClient: sharedRestClient,
			Profile:    mcpserver.ProfileCore,
		})

		// Build SSE transport servers. advertiseOptions decides which URL each
		// server hands back in its `endpoint` event — see the comment on that
		// function for why this is not simply the listen address.
		// A fresh slice per server: appending to one shared slice would let the
		// two servers share a backing array.
		sseOpts := func(basePath string) []sdkserver.SSEOption {
			opts := []sdkserver.SSEOption{
				sdkserver.WithKeepAlive(true),
				sdkserver.WithSSEContextFunc(sseContextFunc),
			}
			return append(opts, advertiseOptions(publicURL, basePath)...)
		}

		fullSSE := sdkserver.NewSSEServer(fullSrv.MCPServer(), sseOpts("")...)
		coreSSE := sdkserver.NewSSEServer(coreSrv.MCPServer(), sseOpts(coreBasePath)...)

		// Build HTTP mux with auth wrappers.
		mux := http.NewServeMux()

		// Prometheus metrics, gated by MESH_METRICS_TOKEN when set — same
		// variable and file-fallback as the API's /metrics (config.Load
		// reads MESH_METRICS_TOKEN / MESH_METRICS_TOKEN_FILE), same no-op-
		// when-empty behavior via middleware.MetricsAuthHTTP. An empty token
		// leaves this open for deployments that gate it at the network layer
		// instead (e.g. internal prod, fronted by Caddy).
		metricsToken := config.Load().Server.MetricsToken
		mux.Handle("/metrics", middleware.MetricsAuthHTTP(metricsToken, promhttp.Handler()))

		// Full profile: /sse and /message (backward compatible).
		mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
			key := extractAgentKeyFromRequest(r)
			if key == "" {
				http.Error(w, "Missing agent key: provide Authorization: Bearer agk_..., X-Agent-Key header, or ?agent_key query param", http.StatusUnauthorized)
				return
			}

			// Validate the key at connection time to fail fast.
			_, err := sessionCache.GetOrAuthenticate(r.Context(), key)
			if err != nil {
				log.Printf("SSE connection auth failed for key %s...: %v", safeKeyPrefix(key), err)
				http.Error(w, fmt.Sprintf("Authentication failed: %v", err), http.StatusForbidden)
				return
			}

			fullSSE.SSEHandler().ServeHTTP(w, r)
		})
		mux.Handle("/message", fullSSE.MessageHandler())

		// Core profile: /core/sse and /core/message.
		mux.HandleFunc(coreBasePath+"/sse", func(w http.ResponseWriter, r *http.Request) {
			key := extractAgentKeyFromRequest(r)
			if key == "" {
				http.Error(w, "Missing agent key: provide Authorization: Bearer agk_..., X-Agent-Key header, or ?agent_key query param", http.StatusUnauthorized)
				return
			}

			// Validate the key at connection time to fail fast.
			_, err := sessionCache.GetOrAuthenticate(r.Context(), key)
			if err != nil {
				log.Printf("SSE core connection auth failed for key %s...: %v", safeKeyPrefix(key), err)
				http.Error(w, fmt.Sprintf("Authentication failed: %v", err), http.StatusForbidden)
				return
			}

			coreSSE.SSEHandler().ServeHTTP(w, r)
		})
		mux.Handle(coreBasePath+"/message", coreSSE.MessageHandler())

		log.Printf("Starting MCP SSE server on %s (multi-agent mode)", addr)
		log.Printf("  Full profile SSE endpoint: %s/sse", dialableURL(publicURL, host, port))
		log.Printf("  Core profile SSE endpoint: %s%s/sse", dialableURL(publicURL, host, port), coreBasePath)
		if publicURL == "" {
			log.Printf("  Message endpoint is advertised relative to the URL each client connects to.")
			log.Printf("  Set MESH_MCP_PUBLIC_URL if your clients require an absolute endpoint URL.")
		} else {
			log.Printf("  Message endpoint is advertised under MESH_MCP_PUBLIC_URL=%s", publicURL)
		}
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

// coreBasePath is the path prefix the lightweight (core-profile) SSE endpoints
// are mounted at. It has to be known to both the mux and the URL the server
// advertises to clients, so it lives in one place.
const coreBasePath = "/core"

// advertiseOptions configures which URL the SSE server hands a client in its
// `endpoint` event — the address the client will POST every subsequent JSON-RPC
// message to.
//
// This is deliberately not derived from the listen address. The server listens
// on 0.0.0.0 so that a published container port works at all, but 0.0.0.0 is a
// wildcard bind, not a destination: a client told to POST to
// http://0.0.0.0:8081/message has been handed an address it cannot dial. That
// is what made a correctly published MCP port look unreachable from outside the
// host while working fine from inside it.
//
// With MESH_MCP_PUBLIC_URL unset we advertise a relative path ("/message?...").
// Every MCP client resolves it against the URL it connected to, so the answer is
// automatically correct for localhost, for a published container port, and for
// any reverse proxy — none of which the server can guess on its own.
//
// Set MESH_MCP_PUBLIC_URL (e.g. https://mesh.example.com/mcp) to advertise
// absolute URLs instead, for clients that reject relative endpoints or for a
// proxy that rewrites the path.
func advertiseOptions(publicURL, basePath string) []sdkserver.SSEOption {
	var opts []sdkserver.SSEOption
	if basePath != "" {
		opts = append(opts, sdkserver.WithStaticBasePath(basePath))
	}
	if publicURL == "" {
		return append(opts, sdkserver.WithUseFullURLForMessageEndpoint(false))
	}
	return append(opts, sdkserver.WithBaseURL(strings.TrimSuffix(publicURL, "/")))
}

// dialableURL returns a URL an operator can paste into a client, for logging
// only. A wildcard listen host is reported as localhost, because that is the
// address that actually works from the machine reading the log.
func dialableURL(publicURL, host, port string) string {
	if publicURL != "" {
		return strings.TrimSuffix(publicURL, "/")
	}
	switch host {
	case "0.0.0.0", "::", "[::]", "":
		host = "localhost"
	}
	return fmt.Sprintf("http://%s:%s", host, port)
}

// buildSession creates an AgentSession from API response strings.
func buildSession(agentID, workspaceID, agentName, agentType string) (*mcpserver.AgentSession, error) {
	session, err := mcpserver.NewAgentSession(agentID, workspaceID, agentName, agentType)
	if err != nil {
		return nil, err
	}
	return &session, nil
}

// extractAgentKeyFromRequest extracts the agent API key from an HTTP request.
func extractAgentKeyFromRequest(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		const bearerPrefix = "Bearer "
		if strings.HasPrefix(auth, bearerPrefix) {
			token := strings.TrimSpace(auth[len(bearerPrefix):])
			if strings.HasPrefix(token, "agk_") {
				return token
			}
		}
	}
	if key := r.Header.Get("X-Agent-Key"); key != "" && strings.HasPrefix(key, "agk_") {
		return key
	}
	if key := r.URL.Query().Get("agent_key"); key != "" && strings.HasPrefix(key, "agk_") {
		return key
	}
	return ""
}

// safeKeyPrefix returns a safe prefix of the key for logging.
func safeKeyPrefix(key string) string {
	if len(key) > 12 {
		return key[:12]
	}
	return key
}

// sessionCacheEntry wraps an authenticated agent session with a last-used
// timestamp so that the cleanup goroutine can evict stale entries.
type sessionCacheEntry struct {
	session  *mcpserver.AgentSession
	lastUsed time.Time
}

// agentSessionCache caches authenticated agent sessions by agent key.
type agentSessionCache struct {
	mu     sync.RWMutex
	cache  map[string]*sessionCacheEntry
	apiURL string
}

// GetOrAuthenticate returns a cached session or authenticates and caches it.
// It updates lastUsed on every hit so that active agents are never evicted.
func (c *agentSessionCache) GetOrAuthenticate(ctx context.Context, key string) (*mcpserver.AgentSession, error) {
	c.mu.RLock()
	if c.cache != nil {
		if entry, ok := c.cache[key]; ok {
			c.mu.RUnlock()
			// Update lastUsed under write lock to record the access time.
			c.mu.Lock()
			entry.lastUsed = time.Now()
			c.mu.Unlock()
			return entry.session, nil
		}
	}
	c.mu.RUnlock()

	// Authenticate via REST API.
	client := mcpserver.NewRESTClient(c.apiURL, key)
	agentInfo, err := client.GetAgentMe(ctx)
	if err != nil {
		return nil, fmt.Errorf("authentication failed: %w", err)
	}

	agentID, _ := agentInfo["id"].(string)
	workspaceID, _ := agentInfo["workspace_id"].(string)
	agentName, _ := agentInfo["name"].(string)
	agentType, _ := agentInfo["agent_type"].(string)

	session, err := mcpserver.NewAgentSession(agentID, workspaceID, agentName, agentType)
	if err != nil {
		return nil, fmt.Errorf("invalid agent data: %w", err)
	}

	c.mu.Lock()
	if c.cache == nil {
		c.cache = make(map[string]*sessionCacheEntry)
	}
	c.cache[key] = &sessionCacheEntry{session: &session, lastUsed: time.Now()}
	c.mu.Unlock()

	log.Printf("SSE: authenticated agent %s (ID: %s)", agentName, agentID)
	return &session, nil
}

// cleanup removes session entries that have not been accessed for idleThreshold.
func (c *agentSessionCache) cleanup(idleThreshold time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	cutoff := time.Now().Add(-idleThreshold)
	for key, entry := range c.cache {
		if entry.lastUsed.Before(cutoff) {
			delete(c.cache, key)
		}
	}
}

// startCleanup spawns a goroutine that periodically evicts stale session entries.
func (c *agentSessionCache) startCleanup(cleanupInterval, idleThreshold time.Duration) {
	go func() {
		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			c.cleanup(idleThreshold)
		}
	}()
}

// clientCacheEntry wraps a per-agent REST client with a last-used timestamp
// so that the cleanup goroutine can evict stale entries.
type clientCacheEntry struct {
	client   *mcpserver.RESTClient
	lastUsed time.Time
}

// serverRegistry caches per-agent REST clients keyed by agent API key.
type serverRegistry struct {
	mu     sync.RWMutex
	cache  map[string]*clientCacheEntry
	apiURL string
}

// GetClient returns a cached REST client for the given agent key, creating one
// if needed. It updates lastUsed on every hit so active agents are never evicted.
func (r *serverRegistry) GetClient(key string) *mcpserver.RESTClient {
	r.mu.RLock()
	if r.cache != nil {
		if entry, ok := r.cache[key]; ok {
			r.mu.RUnlock()
			// Update lastUsed under write lock to record the access time.
			r.mu.Lock()
			entry.lastUsed = time.Now()
			r.mu.Unlock()
			return entry.client
		}
	}
	r.mu.RUnlock()

	client := mcpserver.NewRESTClient(r.apiURL, key)

	r.mu.Lock()
	if r.cache == nil {
		r.cache = make(map[string]*clientCacheEntry)
	}
	r.cache[key] = &clientCacheEntry{client: client, lastUsed: time.Now()}
	r.mu.Unlock()

	return client
}

// cleanup removes client entries that have not been accessed for idleThreshold.
func (r *serverRegistry) cleanup(idleThreshold time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cutoff := time.Now().Add(-idleThreshold)
	for key, entry := range r.cache {
		if entry.lastUsed.Before(cutoff) {
			delete(r.cache, key)
		}
	}
}

// startCleanup spawns a goroutine that periodically evicts stale client entries.
func (r *serverRegistry) startCleanup(cleanupInterval, idleThreshold time.Duration) {
	go func() {
		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			r.cleanup(idleThreshold)
		}
	}()
}
