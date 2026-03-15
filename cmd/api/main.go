package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/pressly/goose/v3"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/redis/go-redis/v9"

	"github.com/entire-vc/evc-mesh/internal/auth"
	"github.com/entire-vc/evc-mesh/internal/config"
	"github.com/entire-vc/evc-mesh/internal/embedding"
	"github.com/entire-vc/evc-mesh/internal/eventbus"
	"github.com/entire-vc/evc-mesh/internal/handler"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/internal/spark"
	"github.com/entire-vc/evc-mesh/internal/storage"
	wsHub "github.com/entire-vc/evc-mesh/internal/ws"
)

func main() {
	// 1. Load configuration from environment.
	cfg := config.Load()

	// 2. Connect to PostgreSQL.
	db, err := postgres.NewDB(cfg.Database.DSN())
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()
	log.Println("Connected to PostgreSQL")

	// 3. Run database migrations.
	if err := goose.Up(db.DB, "migrations"); err != nil {
		log.Fatalf("Failed to run migrations: %v", err)
	}
	log.Println("Database migrations applied")

	// 4. Create all repository instances.
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
	userRepo := postgres.NewUserRepo(db)
	refreshTokenRepo := postgres.NewRefreshTokenRepo(db)
	workspaceMemberRepo := postgres.NewWorkspaceMemberRepo(db)
	projectMemberRepo := postgres.NewProjectMemberRepo(db)
	webhookRepo := postgres.NewWebhookRepo(db)
	savedViewRepo := postgres.NewSavedViewRepo(db)
	vcsLinkRepo := postgres.NewVCSLinkRepo(db)
	integrationRepo := postgres.NewIntegrationRepo(db)
	projectUpdateRepo := postgres.NewProjectUpdateRepo(db)
	initiativeRepo := postgres.NewInitiativeRepo(db)
	ruleRepo := postgres.NewRuleRepo(db)
	wsRuleRepo := postgres.NewWorkspaceRuleRepo(db)
	projRuleRepo := postgres.NewProjectRuleRepo(db)
	ruleViolationLogRepo := postgres.NewRuleViolationLogRepo(db)
	recurringRepo := postgres.NewRecurringRepo(db)
	taskTemplateRepo := postgres.NewTaskTemplateRepo(db)
	notificationRepo := postgres.NewNotificationRepo(db)
	autoTransRuleRepo := postgres.NewAutoTransitionRuleRepo(db)
	memoryRepo := postgres.NewMemoryRepo(db)

	// 5. Create auth service.
	authService := auth.NewService(
		userRepo,
		refreshTokenRepo,
		workspaceRepo,
		workspaceMemberRepo,
		cfg.Auth.JWTSecret,
	)

	// 6. Create all service instances.
	workspaceService := service.NewWorkspaceService(workspaceRepo, activityLogRepo)
	projectService := service.NewProjectService(projectRepo, taskStatusRepo, activityLogRepo,
		service.WithProjectMemberRepo(projectMemberRepo),
		service.WithAutoTransRuleRepo(autoTransRuleRepo),
	)
	customFieldDefRepo := postgres.NewCustomFieldDefinitionRepo(db)
	customFieldService := service.NewCustomFieldService(customFieldDefRepo, activityLogRepo)

	// Rule service is created before taskService so it can be injected as an option.
	ruleService := service.NewRuleService(ruleRepo, activityLogRepo,
		service.WithRuleCommentRepo(commentRepo),
		service.WithRuleTaskRepo(taskRepo),
	)

	// Event bus service is created early so it can be injected into taskService.
	// Task mutations (create/update/move/delete) will auto-publish events.
	eventBusService := service.NewEventBusService(eventBusRepo, activityLogRepo)

	// Embedding provider for vector search (optional; degrades to keyword-only when "none").
	embedder := embedding.NewEmbedder(cfg.Embedding)
	log.Printf("Embedding provider: %s", cfg.Embedding.Provider)

	// Memory service is wired into eventBusService so Publish() can extract memories.
	memoryService := service.NewMemoryService(memoryRepo, embedder)

	// Slack service sends notifications via Slack Incoming Webhooks when a workspace has
	// an active Slack integration configured. It is injected into webhookService below.
	slackService := service.NewSlackService(integrationRepo)

	// Webhook service is created before taskService so it can be injected for agent wakeup dispatch.
	// SlackService is co-injected so that every Dispatch call also notifies Slack when configured.
	webhookService := service.NewWebhookService(webhookRepo, service.WithSlackService(slackService))

	agentActLogRepo := postgres.NewAgentActivityLogRepo(db)
	agentService := service.NewAgentService(agentRepo, activityLogRepo, workspaceRepo)
	// Wire agent activity log repository for monitoring.
	if configurable, ok := agentService.(service.AgentServiceConfigurable); ok {
		configurable.SetAgentActivityLogRepo(agentActLogRepo)
	}

	// Agent notification service for push mechanisms (callback_url, SSE, long-poll).
	// Reuses the same Redis connection as the WebSocket hub (created below in step 8a).
	// We create a dedicated client here so the notify service can be injected into taskService
	// before wsRedis is declared later in main.
	agentNotifyRedis := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr(),
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
	agentNotifySvc := service.NewAgentNotifyService(agentService, agentNotifyRedis)

	// Context cache for GET /tasks/:task_id/context (60-second TTL).
	// A dedicated Redis client is used so it can be closed independently.
	ctxCacheRedis := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr(),
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
	ctxCacheSvc := service.NewContextCacheService(ctxCacheRedis)

	// RulesService (assignment/workflow config) is created before taskService for auto-assign injection.
	rulesService := service.NewRulesServiceWithOptions(wsRuleRepo, projRuleRepo, ruleViolationLogRepo, agentRepo, workspaceMemberRepo, workspaceRepo, projectRepo,
		service.WithRulesRuleRepo(ruleRepo),
	)

	// Notification service for in-app push notifications to workspace users.
	// Created before taskService and commentService so it can be injected as a dependency.
	notificationService := service.NewNotificationService(notificationRepo)

	taskService := service.NewTaskService(taskRepo, taskStatusRepo, taskDependencyRepo, activityLogRepo,
		service.WithCustomFieldService(customFieldService),
		service.WithProjectRepo(projectRepo),
		service.WithRuleService(ruleService),
		service.WithEventBusService(eventBusService),
		service.WithWebhookService(webhookService),
		service.WithAgentNotifyService(agentNotifySvc),
		service.WithRulesConfigService(rulesService),
		service.WithContextCacheInvalidator(ctxCacheSvc),
		service.WithNotificationService(notificationService),
		service.WithProjectMemberRepoTask(projectMemberRepo),
	)

	// Wire auto-transition service. It calls taskService.MoveTask, so taskService must already
	// exist. We inject it back via the configurable interface to trigger transitions on status
	// changes without introducing an import cycle.
	autoTransitionSvc := service.NewAutoTransitionService(taskRepo, taskStatusRepo, taskDependencyRepo, taskService, autoTransRuleRepo)
	if configurable, ok := taskService.(service.TaskServiceAutoTransitionConfigurable); ok {
		configurable.SetAutoTransitionService(autoTransitionSvc)
	}

	taskStatusService := service.NewTaskStatusService(taskStatusRepo, taskRepo, activityLogRepo)

	// Real service implementations (replacing stubs from earlier sprints).
	commentService := service.NewCommentService(commentRepo, taskRepo, activityLogRepo,
		service.WithCommentAgentNotify(agentNotifySvc),
		service.WithCommentStatusRepo(taskStatusRepo),
		service.WithCommentProjectRepo(projectRepo),
		service.WithCommentContextCacheInvalidator(ctxCacheSvc),
		service.WithCommentNotificationService(notificationService),
	)
	depService := service.NewTaskDependencyService(taskDependencyRepo, taskRepo, activityLogRepo)
	activityLogService := service.NewActivityLogService(activityLogRepo)

	// Member services.
	workspaceMemberService := service.NewWorkspaceMemberService(workspaceMemberRepo, userRepo, projectMemberRepo, activityLogRepo)
	projectMemberService := service.NewProjectMemberService(projectMemberRepo, workspaceMemberRepo, projectRepo,
		service.WithAgentRepo(agentRepo),
	)
	savedViewService := service.NewSavedViewService(savedViewRepo)
	vcsLinkService := service.NewVCSLinkService(vcsLinkRepo)
	integrationService := service.NewIntegrationService(integrationRepo)
	analyticsService := service.NewAnalyticsService(db)
	projectUpdateService := service.NewProjectUpdateService(projectUpdateRepo, projectRepo, taskRepo, taskStatusRepo)
	initiativeService := service.NewInitiativeService(initiativeRepo, projectRepo, db)
	triageService := service.NewTriageService(taskRepo)

	recurringService := service.NewRecurringService(recurringRepo, taskService,
		service.WithCommentRepoForRecurring(commentRepo),
		service.WithArtifactRepoForRecurring(artifactRepo),
	)

	taskTemplateService := service.NewTaskTemplateService(taskTemplateRepo, taskService)

	// rulesService, customFieldService, and notificationService were already created above (before taskService).

	// Initialize S3 storage client for artifacts.
	var artifactService service.ArtifactService
	s3Client, s3Err := storage.NewS3Client(
		cfg.S3.Endpoint,
		cfg.S3.AccessKeyID,
		cfg.S3.SecretAccessKey,
		cfg.S3.Bucket,
		cfg.S3.Region,
		cfg.S3.UseSSL,
	)
	if s3Err != nil {
		log.Printf("WARNING: S3 storage unavailable, artifact uploads will fail: %v", s3Err)
		// Use a nil-storage artifact service that will return errors on upload.
		// This is intentional: we no longer use a stub that silently discards uploads.
		artifactService = service.NewArtifactService(artifactRepo, nil, activityLogRepo)
	} else {
		if cfg.S3.PublicURL != "" {
			s3Client.SetPublicURL(cfg.S3.PublicURL)
			log.Printf("S3 presigned URLs will use public URL: %s", cfg.S3.PublicURL)
		}
		artifactService = service.NewArtifactService(artifactRepo, s3Client, activityLogRepo)
	}

	// 6a. Connect to NATS and Redis for the event bus (graceful: continue without if unavailable).
	var eb *eventbus.EventBus
	ebCfg := eventbus.EventBusConfig{
		NATSUrl:       cfg.NATS.URL,
		RedisAddr:     cfg.Redis.Addr(),
		RedisPassword: cfg.Redis.Password,
		RedisDB:       cfg.Redis.DB,
	}
	eb, err = eventbus.New(context.Background(), ebCfg, eventBusRepo)
	if err != nil {
		log.Printf("WARNING: Event bus unavailable, running without NATS/Redis: %v", err)
		eb = nil
	} else {
		// Wire the event bus publisher into the event bus service.
		if configurable, ok := eventBusService.(service.EventBusServiceConfigurable); ok {
			configurable.SetEventBus(eb, workspaceRepo, projectRepo)
		}
		// Start background workers (PG writer + cleanup).
		eb.Start()
	}
	// Wire memory service into eventBusService for memory extraction on Publish().
	// Done outside the NATS block so memory extraction works even without NATS.
	if configurable, ok := eventBusService.(service.EventBusServiceConfigurable); ok {
		configurable.SetMemoryService(memoryService)
	}

	// 7. Create all handler instances.
	authHandler := handler.NewAuthHandler(authService)
	workspaceHandler := handler.NewWorkspaceHandler(workspaceService)
	projectHandler := handler.NewProjectHandler(projectService)
	taskHandler := handler.NewTaskHandler(taskService)
	statusHandler := handler.NewTaskStatusHandler(taskStatusService)
	commentHandler := handler.NewCommentHandler(commentService)
	artifactHandler := handler.NewArtifactHandler(artifactService)
	depHandler := handler.NewDependencyHandler(depService, taskService)
	agentHandler := handler.NewAgentHandlerFull(agentService, taskService, agentNotifyRedis)
	eventHandler := handler.NewEventHandler(eventBusService)
	activityHandler := handler.NewActivityHandler(activityLogService)
	customFieldHandler := handler.NewCustomFieldHandler(customFieldService)
	taskContextHandler := handler.NewTaskContextHandlerWithCache(taskService, commentService, artifactService, depService, eventBusService, ctxCacheSvc, initiativeRepo)
	webhookHandler := handler.NewWebhookHandler(webhookService)
	savedViewHandler := handler.NewSavedViewHandler(savedViewService)
	vcsLinkHandler := handler.NewVCSLinkHandlerWithSecret(vcsLinkService, cfg.Webhook.GitHubSecret)
	integrationHandler := handler.NewIntegrationHandler(integrationService)
	analyticsHandler := handler.NewAnalyticsHandler(analyticsService)
	projectUpdateHandler := handler.NewProjectUpdateHandler(projectUpdateService)
	initiativeHandler := handler.NewInitiativeHandler(initiativeService)
	triageHandler := handler.NewTriageHandler(triageService)
	ruleHandler := handler.NewRuleHandler(ruleService)
	rulesHandler := handler.NewRulesHandler(rulesService)
	recurringHandler := handler.NewRecurringHandler(recurringService)
	taskTemplateHandler := handler.NewTaskTemplateHandler(taskTemplateService)
	workspaceMemberHandler := handler.NewWorkspaceMemberHandler(workspaceMemberService)
	projectMemberHandler := handler.NewProjectMemberHandler(projectMemberService)
	notificationHandler := handler.NewNotificationHandler(notificationService)
	autoTransHandler := handler.NewAutoTransitionHandler(autoTransitionSvc)
	memoryHandler := handler.NewMemoryHandler(memoryService)

	// 8. Create Echo instance with global middleware.
	e := echo.New()
	e.HideBanner = true

	// Prometheus metrics — registered early so every request is counted.
	e.Use(mw.Metrics())

	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogURI:    true,
		LogStatus: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			log.Printf("%s %s -> %d", c.Request().Method, v.URI, v.Status)
			return nil
		},
	}))
	e.Use(middleware.Recover())
	e.Use(middleware.RequestID())
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: cfg.CORS.AllowOrigins,
		AllowMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders: []string{"Authorization", "Content-Type", "X-Agent-Key", "X-Request-ID"},
	}))

	// Prometheus metrics scrape endpoint (unauthenticated, bind to internal network in prod).
	e.GET("/metrics", echo.WrapHandler(promhttp.Handler()))

	// Health check.
	e.GET("/health", func(c echo.Context) error {
		return c.JSON(200, map[string]string{
			"status":  "ok",
			"service": "evc-mesh-api",
		})
	})

	// 8a. Shared Redis client used by the WebSocket hub and the rate limiter.
	// A single client is created here so all consumers share the same connection
	// pool rather than each opening independent connections.
	sharedRedis := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr(),
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})

	// WebSocket Hub for real-time event streaming.
	wsRedis := sharedRedis
	hub := wsHub.NewHub(wsRedis)
	hubCtx, hubCancel := context.WithCancel(context.Background())
	go hub.Run(hubCtx)
	log.Println("WebSocket hub started")

	// WebSocket upgrade endpoint (before auth middleware, auth is handled in the handler).
	e.GET("/ws", wsHub.Handler(hub, authService, agentService))

	// 9. Register all routes.
	v1 := e.Group("/api/v1")

	// --- Public routes (no auth required) ---
	authGroup := v1.Group("/auth")
	// Rate-limit auth endpoints by IP to prevent brute-force attacks.
	// Uses the Redis-backed sliding window limiter for multi-instance deployments.
	authGroup.Use(mw.RateLimit(mw.RateLimitConfig{
		Enabled:     cfg.RateLimit.Enabled,
		RPM:         cfg.RateLimit.AuthRPM,
		KeyFunc:     mw.RateLimitKeyByIP,
		RedisClient: sharedRedis,
	}))
	authGroup.POST("/register", authHandler.Register)
	authGroup.POST("/login", authHandler.Login)
	authGroup.POST("/refresh", authHandler.Refresh)

	// --- Protected routes (JWT or Agent Key) ---
	api := v1.Group("")
	api.Use(mw.DualAuth(authService, agentService))
	api.Use(mw.WorkspaceRLS(db, projectRepo))
	// Rate-limit API endpoints by authenticated actor (user/agent ID).
	// Uses the Redis-backed sliding window limiter for multi-instance deployments.
	api.Use(mw.RateLimit(mw.RateLimitConfig{
		Enabled:     cfg.RateLimit.Enabled,
		RPM:         cfg.RateLimit.APIRPM,
		KeyFunc:     mw.RateLimitKeyByActor,
		RedisClient: sharedRedis,
	}))

	// Auth - protected.
	api.GET("/auth/me", authHandler.Me)
	api.POST("/auth/logout", authHandler.Logout)

	// rbac is a shorthand helper to create per-route RBAC middleware.
	rbac := func(perm mw.Permission) echo.MiddlewareFunc {
		return mw.RequirePermission(perm, workspaceMemberRepo)
	}

	// Workspace routes.
	api.GET("/workspaces", workspaceHandler.List)
	api.POST("/workspaces", workspaceHandler.Create)
	api.GET("/workspaces/:ws_id", workspaceHandler.GetByID)
	api.PATCH("/workspaces/:ws_id", workspaceHandler.Update)
	api.DELETE("/workspaces/:ws_id", workspaceHandler.Delete, rbac(mw.PermDeleteWorkspace))

	// Workspace member routes.
	// NOTE: /members/me MUST be registered before /members/:user_id to avoid "me" being parsed as UUID.
	api.GET("/workspaces/:ws_id/members", workspaceMemberHandler.List)
	api.GET("/workspaces/:ws_id/members/me", workspaceMemberHandler.Me)
	api.POST("/workspaces/:ws_id/members", workspaceMemberHandler.Add, rbac(mw.PermManageMembers))
	api.PATCH("/workspaces/:ws_id/members/:user_id", workspaceMemberHandler.UpdateRole, rbac(mw.PermManageMembers))
	api.DELETE("/workspaces/:ws_id/members/:user_id", workspaceMemberHandler.Remove, rbac(mw.PermManageMembers))
	api.GET("/workspaces/:ws_id/users/search", workspaceMemberHandler.SearchUsers)

	// Project routes.
	api.GET("/workspaces/:ws_id/projects", projectHandler.List)
	api.POST("/workspaces/:ws_id/projects", projectHandler.Create, rbac(mw.PermCreateProject))

	// Project-scoped routes — RequireProjectMember enforces membership for :proj_id routes.
	projAccess := mw.RequireProjectMember(db)
	api.GET("/projects/:proj_id", projectHandler.GetByID, projAccess)
	api.PATCH("/projects/:proj_id", projectHandler.Update, projAccess)
	api.DELETE("/projects/:proj_id", projectHandler.Delete, projAccess, rbac(mw.PermDeleteProject))

	// Project member routes.
	api.GET("/projects/:proj_id/members", projectMemberHandler.List, projAccess)
	api.POST("/projects/:proj_id/members", projectMemberHandler.Add, projAccess, rbac(mw.PermManageMembers))
	api.POST("/projects/:proj_id/members/agents", projectMemberHandler.AddAgent, projAccess, rbac(mw.PermManageMembers))
	api.PATCH("/projects/:proj_id/members/:user_id", projectMemberHandler.UpdateRole, projAccess, rbac(mw.PermManageMembers))
	api.DELETE("/projects/:proj_id/members/:user_id", projectMemberHandler.Remove, projAccess, rbac(mw.PermManageMembers))
	api.DELETE("/projects/:proj_id/members/agents/:member_agent_id", projectMemberHandler.RemoveAgent, projAccess, rbac(mw.PermManageMembers))

	// Task status routes.
	api.GET("/projects/:proj_id/statuses", statusHandler.List, projAccess)
	api.POST("/projects/:proj_id/statuses", statusHandler.Create, projAccess)
	api.PATCH("/projects/:proj_id/statuses/:status_id", statusHandler.Update, projAccess)
	api.PUT("/projects/:proj_id/statuses/reorder", statusHandler.Reorder, projAccess)

	// Custom field routes.
	api.GET("/projects/:proj_id/custom-fields", customFieldHandler.List, projAccess)
	api.POST("/projects/:proj_id/custom-fields", customFieldHandler.Create, projAccess, rbac(mw.PermManageCF))
	api.GET("/custom-fields/:field_id", customFieldHandler.GetByID)
	api.PATCH("/custom-fields/:field_id", customFieldHandler.Update, rbac(mw.PermManageCF))
	api.DELETE("/custom-fields/:field_id", customFieldHandler.Delete, rbac(mw.PermManageCF))
	api.PUT("/projects/:proj_id/custom-fields/reorder", customFieldHandler.Reorder, projAccess, rbac(mw.PermManageCF))

	// Task routes.
	api.GET("/projects/:proj_id/tasks", taskHandler.List, projAccess)
	api.POST("/projects/:proj_id/tasks", taskHandler.Create, projAccess, rbac(mw.PermCreateTask))
	api.POST("/projects/:proj_id/tasks/bulk-update", taskHandler.BulkUpdate, projAccess, rbac(mw.PermUpdateTask))
	api.GET("/tasks/:task_id", taskHandler.GetByID)
	api.PATCH("/tasks/:task_id", taskHandler.Update, rbac(mw.PermUpdateTask))
	api.DELETE("/tasks/:task_id", taskHandler.Delete, rbac(mw.PermDeleteTask))
	api.POST("/tasks/:task_id/move", taskHandler.MoveTask, rbac(mw.PermUpdateTask))
	api.POST("/tasks/:task_id/move-to-project", taskHandler.MoveToProject, rbac(mw.PermUpdateTask))
	api.GET("/tasks/:task_id/subtasks", taskHandler.ListSubtasks)
	api.POST("/tasks/:task_id/subtasks", taskHandler.CreateSubtask, rbac(mw.PermCreateTask))
	api.POST("/tasks/:task_id/assign", taskHandler.AssignTask, rbac(mw.PermUpdateTask))
	api.POST("/tasks/:task_id/checkout", taskHandler.Checkout)
	api.DELETE("/tasks/:task_id/checkout", taskHandler.ReleaseCheckout)
	api.PATCH("/tasks/:task_id/checkout", taskHandler.ExtendCheckout)
	api.GET("/tasks/:task_id/context", taskContextHandler.GetTaskContext)

	// Dependency routes.
	api.GET("/tasks/:task_id/dependencies", depHandler.List)
	api.POST("/tasks/:task_id/dependencies", depHandler.Create, rbac(mw.PermUpdateTask))
	api.DELETE("/tasks/:task_id/dependencies/:dep_id", depHandler.Delete, rbac(mw.PermUpdateTask))
	api.GET("/projects/:proj_id/dependency-graph", depHandler.DependencyGraph, projAccess)

	// Comment routes.
	api.GET("/tasks/:task_id/comments", commentHandler.List)
	api.POST("/tasks/:task_id/comments", commentHandler.Create, rbac(mw.PermAddComment))
	api.PATCH("/comments/:comment_id", commentHandler.Update, rbac(mw.PermAddComment))
	api.DELETE("/comments/:comment_id", commentHandler.Delete, rbac(mw.PermAddComment))

	// Artifact routes.
	api.GET("/tasks/:task_id/artifacts", artifactHandler.List)
	api.POST("/tasks/:task_id/artifacts", artifactHandler.Upload, rbac(mw.PermUploadArtifact))
	api.GET("/artifacts/:artifact_id", artifactHandler.GetByID)
	api.GET("/artifacts/:artifact_id/download", artifactHandler.Download)
	api.GET("/tasks/:task_id/artifacts/:artifact_id/download", artifactHandler.Download) // task-scoped alias
	api.DELETE("/artifacts/:artifact_id", artifactHandler.Delete, rbac(mw.PermUploadArtifact))

	// Agent routes.
	// NOTE: /agents/me/* routes MUST be registered before /agents/:agent_id to avoid
	// "me" being parsed as a UUID parameter.
	api.GET("/workspaces/:ws_id/agents", agentHandler.List)
	api.POST("/workspaces/:ws_id/agents", agentHandler.Register, rbac(mw.PermRegisterAgent))
	api.GET("/agents/me", agentHandler.Me)
	api.PATCH("/agents/me", agentHandler.UpdateMe)
	api.GET("/agents/me/tasks", agentHandler.GetMyTasks)
	api.GET("/agents/me/events/stream", agentHandler.EventStream)
	api.GET("/agents/me/tasks/poll", agentHandler.PollTasks)
	api.POST("/agents/heartbeat", agentHandler.Heartbeat)
	api.GET("/agents/:agent_id", agentHandler.GetByID)
	api.PATCH("/agents/:agent_id", agentHandler.Update, rbac(mw.PermDeleteAgent))
	api.DELETE("/agents/:agent_id", agentHandler.Delete, rbac(mw.PermDeleteAgent))
	api.POST("/agents/:agent_id/regenerate-key", agentHandler.RegenerateKey, rbac(mw.PermDeleteAgent))
	api.GET("/agents/:agent_id/sub-agents", agentHandler.ListSubAgents)
	api.GET("/agents/:agent_id/heartbeat", agentHandler.GetAgentHeartbeat)
	api.GET("/agents/:agent_id/activity", agentHandler.ListAgentActivity)
	api.POST("/agents/:agent_id/activity", agentHandler.CreateAgentActivity)
	api.GET("/workspaces/:ws_id/agents/status", agentHandler.GetAgentsStatus)

	// Event bus routes.
	api.GET("/projects/:proj_id/events", eventHandler.List, projAccess)
	api.POST("/projects/:proj_id/events", eventHandler.Create, projAccess, rbac(mw.PermPublishEvent))
	api.GET("/events/:event_id", eventHandler.GetByID)

	// Webhook routes.
	api.POST("/workspaces/:ws_id/webhooks", webhookHandler.Create, rbac(mw.PermManageWebhooks))
	api.GET("/workspaces/:ws_id/webhooks", webhookHandler.List, rbac(mw.PermManageWebhooks))
	api.GET("/webhooks/:webhook_id", webhookHandler.GetByID, rbac(mw.PermManageWebhooks))
	api.PATCH("/webhooks/:webhook_id", webhookHandler.Update, rbac(mw.PermManageWebhooks))
	api.DELETE("/webhooks/:webhook_id", webhookHandler.Delete, rbac(mw.PermManageWebhooks))
	api.GET("/webhooks/:webhook_id/deliveries", webhookHandler.ListDeliveries, rbac(mw.PermManageWebhooks))
	api.POST("/webhooks/:webhook_id/test", webhookHandler.Test, rbac(mw.PermManageWebhooks))

	// Saved view routes.
	api.GET("/projects/:proj_id/views", savedViewHandler.List, projAccess)
	api.POST("/projects/:proj_id/views", savedViewHandler.Create, projAccess)
	api.GET("/views/:view_id", savedViewHandler.GetByID)
	api.PATCH("/views/:view_id", savedViewHandler.Update)
	api.DELETE("/views/:view_id", savedViewHandler.Delete)

	// Activity log routes.
	api.GET("/workspaces/:ws_id/activity", activityHandler.ListByWorkspace, rbac(mw.PermExportAuditLog))
	api.GET("/workspaces/:ws_id/activity/export", activityHandler.Export, rbac(mw.PermExportAuditLog))
	api.GET("/tasks/:task_id/activity", activityHandler.ListByTask)

	// VCS link routes.
	api.GET("/tasks/:task_id/vcs-links", vcsLinkHandler.List)
	api.POST("/tasks/:task_id/vcs-links", vcsLinkHandler.Create, rbac(mw.PermUpdateTask))
	api.DELETE("/vcs-links/:link_id", vcsLinkHandler.Delete, rbac(mw.PermUpdateTask))

	// GitHub webhook receiver (public — no auth, HMAC optional in future).
	e.POST("/webhooks/github", vcsLinkHandler.GitHubWebhook)

	// Integration config routes.
	api.GET("/workspaces/:ws_id/integrations", integrationHandler.List)
	api.POST("/workspaces/:ws_id/integrations", integrationHandler.Configure, rbac(mw.PermManageWebhooks))
	api.PATCH("/integrations/:int_id", integrationHandler.Update, rbac(mw.PermManageWebhooks))
	api.DELETE("/integrations/:int_id", integrationHandler.Delete, rbac(mw.PermManageWebhooks))

	// Analytics routes.
	api.GET("/workspaces/:ws_id/analytics", analyticsHandler.GetMetrics)
	api.GET("/workspaces/:ws_id/analytics/export", analyticsHandler.ExportMetrics)

	// Project update routes.
	api.POST("/projects/:proj_id/updates", projectUpdateHandler.Create, projAccess)
	api.GET("/projects/:proj_id/updates", projectUpdateHandler.List, projAccess)
	api.GET("/projects/:proj_id/updates/latest", projectUpdateHandler.GetLatest, projAccess)

	// Initiative routes.
	api.POST("/workspaces/:ws_id/initiatives", initiativeHandler.Create, rbac(mw.PermCreateProject))
	api.GET("/workspaces/:ws_id/initiatives", initiativeHandler.List)
	api.GET("/initiatives/:init_id", initiativeHandler.GetByID)
	api.PATCH("/initiatives/:init_id", initiativeHandler.Update, rbac(mw.PermCreateProject))
	api.DELETE("/initiatives/:init_id", initiativeHandler.Delete, rbac(mw.PermDeleteProject))
	api.POST("/initiatives/:init_id/projects", initiativeHandler.LinkProject, rbac(mw.PermCreateProject))
	api.DELETE("/initiatives/:init_id/projects/:proj_id", initiativeHandler.UnlinkProject, rbac(mw.PermCreateProject))

	// Triage inbox route.
	api.GET("/workspaces/:ws_id/triage", triageHandler.List)

	// Recurring task schedule routes.
	api.POST("/projects/:proj_id/recurring", recurringHandler.Create, projAccess, rbac(mw.PermCreateTask))
	api.GET("/projects/:proj_id/recurring", recurringHandler.List, projAccess)
	api.GET("/recurring/:id", recurringHandler.GetByID)
	api.PATCH("/recurring/:id", recurringHandler.Update, rbac(mw.PermUpdateTask))
	api.DELETE("/recurring/:id", recurringHandler.Delete, rbac(mw.PermDeleteTask))
	api.POST("/recurring/:id/trigger", recurringHandler.Trigger, rbac(mw.PermCreateTask))
	api.GET("/recurring/:id/history", recurringHandler.History)

	// Task template routes.
	api.POST("/projects/:proj_id/templates", taskTemplateHandler.Create, projAccess, rbac(mw.PermCreateTask))
	api.GET("/projects/:proj_id/templates", taskTemplateHandler.List, projAccess)
	api.GET("/templates/:tmpl_id", taskTemplateHandler.GetByID)
	api.PATCH("/templates/:tmpl_id", taskTemplateHandler.Update, rbac(mw.PermUpdateTask))
	api.DELETE("/templates/:tmpl_id", taskTemplateHandler.Delete, rbac(mw.PermDeleteTask))
	api.POST("/templates/:tmpl_id/create-task", taskTemplateHandler.CreateTask, rbac(mw.PermCreateTask))

	// Team Directory routes (Sprint 20).
	api.GET("/workspaces/:ws_id/team", rulesHandler.GetTeamDirectory)
	api.PUT("/agents/:agent_id/profile", rulesHandler.UpdateAgentProfile)

	// Assignment Rules routes (Sprint 20).
	api.GET("/workspaces/:ws_id/rules/assignment", rulesHandler.GetWorkspaceAssignmentRules)
	api.PUT("/workspaces/:ws_id/rules/assignment", rulesHandler.SetWorkspaceAssignmentRules, rbac(mw.PermManageMembers))
	api.GET("/projects/:proj_id/rules/assignment", rulesHandler.GetEffectiveAssignmentRules, projAccess)
	api.PUT("/projects/:proj_id/rules/assignment", rulesHandler.SetProjectAssignmentRules, projAccess, rbac(mw.PermManageMembers))

	// Workflow Rules routes (Sprint 20).
	api.GET("/projects/:proj_id/rules/workflow", rulesHandler.GetProjectWorkflowRules, projAccess)
	api.PUT("/projects/:proj_id/rules/workflow", rulesHandler.SetProjectWorkflowRules, projAccess, rbac(mw.PermManageMembers))

	// Violation Log routes (Sprint 20).
	api.GET("/workspaces/:ws_id/violations", rulesHandler.ListViolations)

	// Config Import/Export routes (Sprint 21).
	api.POST("/workspaces/:ws_id/config/import", rulesHandler.ImportConfig, rbac(mw.PermManageMembers))
	api.GET("/workspaces/:ws_id/config/export", rulesHandler.ExportConfig)
	api.POST("/workspaces/:ws_id/team/import", rulesHandler.ImportTeam, rbac(mw.PermManageMembers))

	// Workflow Templates routes (Sprint 21).
	api.GET("/workspaces/:ws_id/rules/workflow-templates", rulesHandler.GetWorkflowTemplates)
	api.PUT("/workspaces/:ws_id/rules/workflow-templates", rulesHandler.SetWorkflowTemplates, rbac(mw.PermManageMembers))

	// Governance rule routes.
	api.POST("/workspaces/:ws_id/rules", ruleHandler.CreateWorkspaceRule, rbac(mw.PermManageRules))
	api.GET("/workspaces/:ws_id/rules", ruleHandler.ListWorkspaceRules)
	api.GET("/workspaces/:ws_id/rules/effective", ruleHandler.GetWorkspaceEffectiveRules)
	api.POST("/projects/:proj_id/rules", ruleHandler.CreateProjectRule, projAccess, rbac(mw.PermManageRules))
	api.GET("/projects/:proj_id/rules", ruleHandler.ListProjectRules, projAccess)
	api.GET("/projects/:proj_id/rules/effective", ruleHandler.GetProjectEffectiveRules, projAccess)
	api.GET("/rules/:rule_id", ruleHandler.GetRule)
	api.PATCH("/rules/:rule_id", ruleHandler.UpdateRule, rbac(mw.PermManageRules))
	api.DELETE("/rules/:rule_id", ruleHandler.DeleteRule, rbac(mw.PermManageRules))
	api.POST("/rules/evaluate", ruleHandler.EvaluateRules)

	// Auto-transition rule routes.
	api.GET("/projects/:proj_id/auto-transition-rules", autoTransHandler.List, projAccess)
	api.POST("/projects/:proj_id/auto-transition-rules", autoTransHandler.Create, projAccess, rbac(mw.PermManageRules))
	api.PUT("/projects/:proj_id/auto-transition-rules/:rule_id", autoTransHandler.Update, projAccess, rbac(mw.PermManageRules))
	api.DELETE("/projects/:proj_id/auto-transition-rules/:rule_id", autoTransHandler.Delete, projAccess, rbac(mw.PermManageRules))

	// Notification routes.
	api.GET("/notifications", notificationHandler.List)
	api.POST("/notifications/mark-read", notificationHandler.MarkRead)
	api.GET("/notifications/preferences", notificationHandler.GetPreferences)
	api.PUT("/notifications/preferences", notificationHandler.UpdatePreferences)

	// Memory routes.
	// NOTE: fixed-path routes (/memories/search, /memories/export, /memories/import,
	// /memories/reindex) MUST be registered before /memories/:id to avoid the
	// literal path segments being parsed as UUID parameters.
	api.POST("/memories", memoryHandler.Remember)
	api.GET("/memories", memoryHandler.List)
	api.GET("/memories/search", memoryHandler.Search)
	api.GET("/memories/export", memoryHandler.ExportMemories)
	api.POST("/memories/import", memoryHandler.ImportMemories)
	api.POST("/memories/reindex", memoryHandler.Reindex)
	api.GET("/memories/:id", memoryHandler.GetByID)
	api.GET("/memories/:id/related", memoryHandler.FindRelated)
	api.DELETE("/memories/:id", memoryHandler.Delete)
	api.GET("/projects/:proj_id/knowledge", memoryHandler.GetProjectKnowledge, projAccess)

	// Spark catalog routes (optional; only registered when MESH_SPARK_ENABLED=true).
	if cfg.Spark.Enabled {
		sparkClient := spark.NewClient(cfg.Spark.URL)
		sparkHandler := handler.NewSparkHandler(sparkClient, agentService)
		api.GET("/spark/agents", sparkHandler.Search)
		api.GET("/spark/agents/popular", sparkHandler.Popular)
		api.GET("/spark/agents/:agent_id", sparkHandler.GetByID)
		api.POST("/spark/agents/:agent_id/install", sparkHandler.Install)
		log.Printf("Spark catalog integration enabled (base URL: %s)", cfg.Spark.URL)
	}

	// 10. Start recurring task scheduler.
	schedulerShutdownCh := make(chan struct{})
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				count, err := recurringService.RunDue(ctx)
				cancel()
				if err != nil {
					log.Printf("[scheduler] ERROR running due schedules: %v", err)
				} else if count > 0 {
					log.Printf("[scheduler] Created %d recurring task instances", count)
				}
			case <-schedulerShutdownCh:
				log.Println("[scheduler] Shutting down")
				return
			}
		}
	}()
	log.Println("Recurring task scheduler started (60s interval)")

	// 10a. Memory decay + cleanup scheduler (every 6 hours).
	// DecayRelevance and CleanExpired are idempotent so running frequently is safe.
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				if n, decayErr := memoryRepo.DecayRelevance(ctx); decayErr != nil {
					log.Printf("[memory-decay] ERROR: %v", decayErr)
				} else if n > 0 {
					log.Printf("[memory-decay] Decayed %d stale memories", n)
				}
				if n, cleanErr := memoryRepo.CleanExpired(ctx); cleanErr != nil {
					log.Printf("[memory-cleanup] ERROR: %v", cleanErr)
				} else if n > 0 {
					log.Printf("[memory-cleanup] Cleaned %d expired memories", n)
				}
				cancel()
			case <-schedulerShutdownCh:
				return
			}
		}
	}()
	log.Println("Memory decay scheduler started (6h interval)")

	// 11. Start server with graceful shutdown.
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("Starting evc-mesh API server on %s", addr)

	// Start server in a goroutine.
	go func() {
		if err := e.Start(addr); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	// Wait for interrupt signal.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")

	// Stop the scheduler.
	close(schedulerShutdownCh)

	// Graceful shutdown with timeout.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	if err := e.Shutdown(shutdownCtx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}

	// Close WebSocket hub and shared Redis (also used by the rate limiter).
	hubCancel()
	if err := sharedRedis.Close(); err != nil {
		log.Printf("Error closing shared Redis: %v", err)
	}
	if err := agentNotifyRedis.Close(); err != nil {
		log.Printf("Error closing agent-notify Redis: %v", err)
	}
	if err := ctxCacheRedis.Close(); err != nil {
		log.Printf("Error closing context-cache Redis: %v", err)
	}

	// Close event bus.
	if eb != nil {
		if err := eb.Close(); err != nil {
			log.Printf("Error closing event bus: %v", err)
		}
	}

	log.Println("Server stopped")
}
