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

	"github.com/redis/go-redis/v9"

	"github.com/entire-vc/evc-mesh/internal/auth"
	"github.com/entire-vc/evc-mesh/internal/config"
	"github.com/entire-vc/evc-mesh/internal/eventbus"
	"github.com/entire-vc/evc-mesh/internal/handler"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
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
	projectService := service.NewProjectService(projectRepo, taskStatusRepo, activityLogRepo)
	customFieldDefRepo := postgres.NewCustomFieldDefinitionRepo(db)
	customFieldService := service.NewCustomFieldService(customFieldDefRepo, activityLogRepo)

	taskService := service.NewTaskService(taskRepo, taskStatusRepo, taskDependencyRepo, activityLogRepo,
		service.WithCustomFieldService(customFieldService),
	)
	taskStatusService := service.NewTaskStatusService(taskStatusRepo, taskRepo, activityLogRepo)
	agentService := service.NewAgentService(agentRepo, activityLogRepo, workspaceRepo)

	// Real service implementations (replacing stubs from earlier sprints).
	commentService := service.NewCommentService(commentRepo, taskRepo, activityLogRepo)
	depService := service.NewTaskDependencyService(taskDependencyRepo, taskRepo, activityLogRepo)
	eventBusService := service.NewEventBusService(eventBusRepo, activityLogRepo)
	activityLogService := service.NewActivityLogService(activityLogRepo)

	// customFieldService was already created above (before taskService, for CF value validation).

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

	// 7. Create all handler instances.
	authHandler := handler.NewAuthHandler(authService)
	workspaceHandler := handler.NewWorkspaceHandler(workspaceService)
	projectHandler := handler.NewProjectHandler(projectService)
	taskHandler := handler.NewTaskHandler(taskService)
	statusHandler := handler.NewTaskStatusHandler(taskStatusService)
	commentHandler := handler.NewCommentHandler(commentService)
	artifactHandler := handler.NewArtifactHandler(artifactService)
	depHandler := handler.NewDependencyHandler(depService, taskService)
	agentHandler := handler.NewAgentHandler(agentService)
	eventHandler := handler.NewEventHandler(eventBusService)
	activityHandler := handler.NewActivityHandler(activityLogService)
	customFieldHandler := handler.NewCustomFieldHandler(customFieldService)

	// 8. Create Echo instance with global middleware.
	e := echo.New()
	e.HideBanner = true

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
		AllowOrigins: []string{"*"},
		AllowMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders: []string{"Authorization", "Content-Type", "X-Agent-Key", "X-Request-ID"},
	}))

	// Health check.
	e.GET("/health", func(c echo.Context) error {
		return c.JSON(200, map[string]string{
			"status":  "ok",
			"service": "evc-mesh-api",
		})
	})

	// 8a. WebSocket Hub for real-time event streaming.
	wsRedis := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr(),
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
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
	authGroup.POST("/register", authHandler.Register)
	authGroup.POST("/login", authHandler.Login)
	authGroup.POST("/refresh", authHandler.Refresh)

	// --- Protected routes (JWT or Agent Key) ---
	api := v1.Group("")
	api.Use(mw.DualAuth(authService, agentService))
	api.Use(mw.WorkspaceRLS(db, projectRepo))

	// Auth - protected.
	api.GET("/auth/me", authHandler.Me)
	api.POST("/auth/logout", authHandler.Logout)

	// Workspace routes.
	api.GET("/workspaces", workspaceHandler.List)
	api.POST("/workspaces", workspaceHandler.Create)
	api.GET("/workspaces/:ws_id", workspaceHandler.GetByID)
	api.PATCH("/workspaces/:ws_id", workspaceHandler.Update)
	api.DELETE("/workspaces/:ws_id", workspaceHandler.Delete)

	// Project routes.
	api.GET("/workspaces/:ws_id/projects", projectHandler.List)
	api.POST("/workspaces/:ws_id/projects", projectHandler.Create)
	api.GET("/projects/:proj_id", projectHandler.GetByID)
	api.PATCH("/projects/:proj_id", projectHandler.Update)
	api.DELETE("/projects/:proj_id", projectHandler.Delete)

	// Task status routes.
	api.GET("/projects/:proj_id/statuses", statusHandler.List)
	api.POST("/projects/:proj_id/statuses", statusHandler.Create)
	api.PATCH("/projects/:proj_id/statuses/:status_id", statusHandler.Update)
	api.PUT("/projects/:proj_id/statuses/reorder", statusHandler.Reorder)

	// Custom field routes.
	api.GET("/projects/:proj_id/custom-fields", customFieldHandler.List)
	api.POST("/projects/:proj_id/custom-fields", customFieldHandler.Create)
	api.GET("/custom-fields/:field_id", customFieldHandler.GetByID)
	api.PATCH("/custom-fields/:field_id", customFieldHandler.Update)
	api.DELETE("/custom-fields/:field_id", customFieldHandler.Delete)
	api.PUT("/projects/:proj_id/custom-fields/reorder", customFieldHandler.Reorder)

	// Task routes.
	api.GET("/projects/:proj_id/tasks", taskHandler.List)
	api.POST("/projects/:proj_id/tasks", taskHandler.Create)
	api.GET("/tasks/:task_id", taskHandler.GetByID)
	api.PATCH("/tasks/:task_id", taskHandler.Update)
	api.DELETE("/tasks/:task_id", taskHandler.Delete)
	api.POST("/tasks/:task_id/move", taskHandler.MoveTask)
	api.GET("/tasks/:task_id/subtasks", taskHandler.ListSubtasks)

	// Dependency routes.
	api.GET("/tasks/:task_id/dependencies", depHandler.List)
	api.POST("/tasks/:task_id/dependencies", depHandler.Create)
	api.DELETE("/tasks/:task_id/dependencies/:dep_id", depHandler.Delete)
	api.GET("/projects/:proj_id/dependency-graph", depHandler.DependencyGraph)

	// Comment routes.
	api.GET("/tasks/:task_id/comments", commentHandler.List)
	api.POST("/tasks/:task_id/comments", commentHandler.Create)
	api.PATCH("/comments/:comment_id", commentHandler.Update)
	api.DELETE("/comments/:comment_id", commentHandler.Delete)

	// Artifact routes.
	api.GET("/tasks/:task_id/artifacts", artifactHandler.List)
	api.POST("/tasks/:task_id/artifacts", artifactHandler.Upload)
	api.GET("/artifacts/:artifact_id", artifactHandler.GetByID)
	api.GET("/artifacts/:artifact_id/download", artifactHandler.Download)
	api.DELETE("/artifacts/:artifact_id", artifactHandler.Delete)

	// Agent routes.
	api.GET("/workspaces/:ws_id/agents", agentHandler.List)
	api.POST("/workspaces/:ws_id/agents", agentHandler.Register)
	api.GET("/agents/:agent_id", agentHandler.GetByID)
	api.PATCH("/agents/:agent_id", agentHandler.Update)
	api.DELETE("/agents/:agent_id", agentHandler.Delete)
	api.POST("/agents/:agent_id/regenerate-key", agentHandler.RegenerateKey)
	api.GET("/agents/me", agentHandler.Me)
	api.POST("/agents/heartbeat", agentHandler.Heartbeat)

	// Event bus routes.
	api.GET("/projects/:proj_id/events", eventHandler.List)
	api.POST("/projects/:proj_id/events", eventHandler.Create)
	api.GET("/events/:event_id", eventHandler.GetByID)

	// Activity log routes.
	api.GET("/workspaces/:ws_id/activity", activityHandler.ListByWorkspace)
	api.GET("/tasks/:task_id/activity", activityHandler.ListByTask)

	// 10. Start server with graceful shutdown.
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

	// Graceful shutdown with timeout.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	if err := e.Shutdown(shutdownCtx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}

	// Close WebSocket hub.
	hubCancel()
	if err := wsRedis.Close(); err != nil {
		log.Printf("Error closing WebSocket Redis: %v", err)
	}

	// Close event bus.
	if eb != nil {
		if err := eb.Close(); err != nil {
			log.Printf("Error closing event bus: %v", err)
		}
	}

	log.Println("Server stopped")
}
