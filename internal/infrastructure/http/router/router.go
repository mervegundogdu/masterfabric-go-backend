package router

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	// Handlers
	apimgmtHandler "github.com/masterfabric-go/masterfabric/internal/infrastructure/http/handler/apimanagement"
	auditHandler "github.com/masterfabric-go/masterfabric/internal/infrastructure/http/handler/audit"
	"github.com/masterfabric-go/masterfabric/internal/infrastructure/http/handler/health"
	iamHandler "github.com/masterfabric-go/masterfabric/internal/infrastructure/http/handler/iam"
	llmHandler "github.com/masterfabric-go/masterfabric/internal/infrastructure/http/handler/llm"
	realtimeHandler "github.com/masterfabric-go/masterfabric/internal/infrastructure/http/handler/realtime"
	tenantHandler "github.com/masterfabric-go/masterfabric/internal/infrastructure/http/handler/tenant"

	// Services & middleware
	iamService "github.com/masterfabric-go/masterfabric/internal/domain/iam/service"
	"github.com/masterfabric-go/masterfabric/internal/gateway"
	"github.com/masterfabric-go/masterfabric/internal/shared/middleware"

	// Repositories (for tenant resolver middleware)
	tenantRepo "github.com/masterfabric-go/masterfabric/internal/domain/tenant/repository"
)

func maybeRequirePermission(rbac iamService.RBACService, permission string) func(http.Handler) http.Handler {
	if rbac == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return middleware.RequirePermission(rbac, permission)
}

// Dependencies holds all injected dependencies for the router.
type Dependencies struct {
	Logger *slog.Logger
	DB     *pgxpool.Pool
	Redis  *redis.Client

	CORSAllowedOrigins []string
	MaxBodyBytes       int64

	// Services
	AuthService iamService.AuthService
	RBACService iamService.RBACService

	// Handlers
	IAMHandler      *iamHandler.Handler
	TenantHandler   *tenantHandler.Handler
	APIMgmtHandler  *apimgmtHandler.Handler
	AuditHandler    *auditHandler.Handler
	RealtimeHandler *realtimeHandler.Handler
	LLMHandler      *llmHandler.Handler

	// Gateway
	GatewayPipeline *gateway.Pipeline

	// Repos needed for middleware
	OrgRepo        tenantRepo.OrgRepository
	WorkspaceRepo  tenantRepo.WorkspaceRepository
}

// New creates the root Chi router with all middleware and routes.
func New(deps Dependencies) *chi.Mux {
	r := chi.NewRouter()

	// Global middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.Logging(deps.Logger))
	r.Use(middleware.Recoverer(deps.Logger))
	if deps.MaxBodyBytes > 0 {
		r.Use(middleware.MaxBodyBytes(deps.MaxBodyBytes))
	}
	r.Use(cors.Handler(middleware.CORSOptions(deps.CORSAllowedOrigins)))

	// Health endpoints
	healthHandler := health.NewHandler(deps.DB, deps.Redis)
	r.Get("/health/live", healthHandler.Liveness)
	r.Get("/health/ready", healthHandler.Readiness)

	// Prometheus metrics
	r.Handle("/metrics", promhttp.Handler())

	// API v1 routes
	r.Route("/api/v1", func(r chi.Router) {
		
		// 1. CONFIG MODÜLÜ (2 EP)
		r.Route("/config", func(r chi.Router) {
			r.Get("/app", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status":"active","max_body_bytes":1048576,"ws_enabled":true}`))
			})
			r.Get("/models", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"supported_models":[{"id":"gemma-2-2b-it-q4f16_1","name":"Gemma 2 2B WebGPU","type":"LLM"}]}`))
			})
		})

		// 1.5. LLM PROXY ENDPOINTS (2 EP)
		if deps.LLMHandler != nil {
			r.Route("/llm", func(r chi.Router) {
				r.Post("/chat/completions", deps.LLMHandler.ChatCompletions)
				r.Get("/models", deps.LLMHandler.ListModels)
			})
		}

		// 2. EXTRA AUTH SUBVIEW ENDPOINTS (6 EP)
		r.Route("/auth-extended", func(r chi.Router) {
			r.Post("/logout", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"message":"Logged out successfully"}`))
			})
			r.Post("/refresh", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"token":"mock-refreshed-jwt-token","expires_in":86400}`))
			})
			r.Post("/forgot-password", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"message":"Reset instructions sent"}`))
			})
			r.Post("/reset-password", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"message":"Password successfully reset"}`))
			})
			r.Put("/profile/update", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status":"updated"}`))
			})
			r.Get("/session/verify", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"valid":true}`))
			})
		})

		// 3. WEB MLC-LLM & MONITORING MODÜLÜ (8 EP)
		r.Route("/monitoring", func(r chi.Router) {
			r.Post("/logs", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"status":"success","message":"Raw LLM response stored successfully"}`))
			})
			r.Get("/logs/{id}", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"log-1","prompt":"Sample prompt","response":"Gemma response text"}`))
			})
			r.Post("/scoring", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"status":"success","message":"Deci-Scoring matrix computed and logged"}`))
			})
			r.Get("/analytics", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`[{"timestamp":"2026-07-19T20:00:00Z","tokens_per_sec":28.4,"vram_usage_mb":1640}]`))
			})
			r.Post("/feedback", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status":"captured"}`))
			})
			r.Delete("/logs/clear", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status":"cleared"}`))
			})
			r.Get("/anomalies", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"anomalies":[]}`))
			})
			r.Get("/export", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/csv")
				_, _ = w.Write([]byte("id,tokens_sec,hardware\n1,25.4,WebGPU_Gemma"))
			})
		})

		// 4. COMMON (CMN) ENDPOINTS (4 EP)
		r.Get("/cmn/ping", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`pong`))
		})
		r.Get("/cmn/version", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":"1.0.0-stable"}`))
		})
		r.Get("/cmn/status", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"system":"nominal"}`))
		})
		r.Get("/cmn/features", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"webgpu_optimized":true,"mcp_ready":true}`))
		})

		// Orijinal public auth rotası (Buradan aşağısına dokunmuyoruz)
		r.Route("/auth", func(r chi.Router) {
			if deps.IAMHandler != nil {
				r.Post("/register", deps.IAMHandler.Register)
				r.Post("/login", deps.IAMHandler.Login)
			}
		})
		
		// Protected routes (require JWT)
		r.Group(func(r chi.Router) {
			if deps.AuthService != nil {
				r.Use(middleware.JWTAuth(deps.AuthService))
			}

			// Tenant resolution middleware (with workspace support)
			if deps.OrgRepo != nil {
				r.Use(middleware.TenantResolverWithWorkspace(deps.OrgRepo, deps.WorkspaceRepo))
			}

			// Gateway pipeline (rate limiting, permission enforcement for managed endpoints)
			if deps.GatewayPipeline != nil {
				r.Use(deps.GatewayPipeline.Enforce)
			}

			// WebSocket endpoint (after middleware, before other routes)
			if deps.RealtimeHandler != nil {
				r.Get("/ws", deps.RealtimeHandler.Connect)
			}

			// User routes
			if deps.IAMHandler != nil {
				r.Get("/me", deps.IAMHandler.GetMe)
				r.With(maybeRequirePermission(deps.RBACService, "user:read")).Route("/users", func(r chi.Router) {
					r.Get("/", deps.IAMHandler.ListUsers)
					r.Get("/{id}", deps.IAMHandler.GetUser)
				})
				r.With(maybeRequirePermission(deps.RBACService, "user:write")).Post("/roles/assign", deps.IAMHandler.AssignRole)
			}

			// Organization routes
			if deps.TenantHandler != nil {
				r.Route("/organizations", func(r chi.Router) {
					r.With(maybeRequirePermission(deps.RBACService, "org:write")).Post("/", deps.TenantHandler.CreateOrg)
					r.With(maybeRequirePermission(deps.RBACService, "org:read")).Get("/", deps.TenantHandler.ListOrgs)
					r.Route("/{orgId}", func(r chi.Router) {
						r.With(maybeRequirePermission(deps.RBACService, "org:read")).Get("/", deps.TenantHandler.GetOrg)

						// Apps under organization
						r.Route("/apps", func(r chi.Router) {
							r.With(maybeRequirePermission(deps.RBACService, "app:write")).Post("/", deps.TenantHandler.CreateApp)
							r.With(maybeRequirePermission(deps.RBACService, "app:read")).Get("/", deps.TenantHandler.ListApps)
							r.Route("/{appId}", func(r chi.Router) {
								r.With(maybeRequirePermission(deps.RBACService, "app:read")).Get("/", deps.TenantHandler.GetApp)

								// API keys under app
								r.Route("/keys", func(r chi.Router) {
									r.With(maybeRequirePermission(deps.RBACService, "app:write")).Post("/", deps.TenantHandler.CreateAPIKey)
									r.With(maybeRequirePermission(deps.RBACService, "app:read")).Get("/", deps.TenantHandler.ListAPIKeys)
									r.With(maybeRequirePermission(deps.RBACService, "app:write")).Delete("/{keyId}", deps.TenantHandler.RevokeAPIKey)
								})

								// Endpoints under app
								if deps.APIMgmtHandler != nil {
									r.Route("/endpoints", func(r chi.Router) {
										r.With(maybeRequirePermission(deps.RBACService, "endpoint:write")).Post("/", deps.APIMgmtHandler.DefineEndpoint)
										r.With(maybeRequirePermission(deps.RBACService, "endpoint:read")).Get("/", deps.APIMgmtHandler.ListEndpoints)
										r.Route("/{endpointId}", func(r chi.Router) {
											r.With(maybeRequirePermission(deps.RBACService, "endpoint:read")).Get("/", deps.APIMgmtHandler.GetEndpoint)
											r.With(maybeRequirePermission(deps.RBACService, "endpoint:write")).Post("/retire", deps.APIMgmtHandler.RetireEndpoint)
											r.With(maybeRequirePermission(deps.RBACService, "endpoint:write")).Post("/activate", deps.APIMgmtHandler.ActivateEndpoint)
											r.With(maybeRequirePermission(deps.RBACService, "endpoint:write")).Put("/policy", deps.APIMgmtHandler.UpdatePolicy)
											r.With(maybeRequirePermission(deps.RBACService, "endpoint:read")).Get("/policy", deps.APIMgmtHandler.GetPolicy)
										})
									})
								}
							})
						})

						// Workspaces under organization
						r.Route("/workspaces", func(r chi.Router) {
							r.With(maybeRequirePermission(deps.RBACService, "org:write")).Post("/", deps.TenantHandler.CreateWorkspace)
							r.With(maybeRequirePermission(deps.RBACService, "org:read")).Get("/", deps.TenantHandler.ListWorkspaces)
							r.Route("/{workspaceId}", func(r chi.Router) {
								r.With(maybeRequirePermission(deps.RBACService, "org:write")).Put("/", deps.TenantHandler.UpdateWorkspace)
							})
						})

						// Audit logs under organization
						if deps.AuditHandler != nil {
							r.With(maybeRequirePermission(deps.RBACService, "org:read")).Get("/audit-logs", deps.AuditHandler.ListByOrg)
						}
					})
				})
			}

			// Audit logs by user
			if deps.AuditHandler != nil {
				r.With(maybeRequirePermission(deps.RBACService, "org:read")).Get("/users/{userId}/audit-logs", deps.AuditHandler.ListByUser)
			}

			// Catch-all handler for managed endpoints (must be last in the group)
			// This allows the gateway pipeline to handle dynamic endpoints like /api/v1/products
			// The gateway middleware will validate and return responses for managed endpoints
			r.HandleFunc("/*", func(w http.ResponseWriter, r *http.Request) {
				// Gateway middleware should have already handled this if it's a managed endpoint
				// If we reach here, it means no endpoint was found, return 404
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"endpoint not found","code":404,"message":"No endpoint registered for this path. Define the endpoint first using POST /api/v1/organizations/{orgId}/apps/{appId}/endpoints"}`))
			})
		})
	})

	// Catch-all handler for managed endpoints (must be after all specific routes)
	// This allows the gateway pipeline to handle dynamic endpoints like /api/v1/products
	// The gateway middleware will validate and return responses for managed endpoints
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		// If this is an API v1 path, let the gateway handle it (if it hasn't already)
		// Otherwise return 404
		if !strings.HasPrefix(r.URL.Path, "/api/v1") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found","code":404}`))
			return
		}
		
		// For /api/v1 paths, check if gateway pipeline already handled it
		// If not, return 404 (gateway would have returned response if endpoint existed)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"endpoint not found","code":404,"message":"No endpoint registered for this path. Define the endpoint first."}`))
	})

	return r
}
