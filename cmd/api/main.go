package main

import (
	_ "embed"
	"context"
	"log"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/helmet"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"

	"tracking-engine/internal/auth"
	"tracking-engine/internal/collector"
	"tracking-engine/internal/config"
	"tracking-engine/internal/integhandler"
	"tracking-engine/internal/storage"
)

//go:embed landing.html
var landingHTML []byte

//go:embed dashboard.html
var dashboardHTML []byte

//go:embed docs.html
var docsHTML []byte

func main() {
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		log.Fatalf("Configuração inválida: %v", err)
	}

	// 1. Conexão com Redis (buffer de streaming e sessões)
	rdb, err := storage.NewRedis(cfg)
	if err != nil {
		log.Fatalf("Erro ao conectar no Redis: %v", err)
	}
	defer rdb.Close()

	// 2. Conexão opcional com PostgreSQL (metadados e chaves)
	pg, err := storage.NewPostgres(cfg)
	if err != nil {
		log.Printf("Aviso: Falha ao conectar no Postgres: %v (seguindo com fallback de chaves)", err)
	} else {
		defer pg.Close()
	}

	// 3. Conexão opcional com ClickHouse (métricas analíticas e telemetria de robôs)
	ch, err := storage.NewClickHouse(cfg)
	if err != nil {
		log.Printf("Aviso: Falha ao conectar no ClickHouse na API: %v (estatísticas analíticas indisponíveis)", err)
	} else {
		defer ch.Close()
	}

	// 4. Inicializa Serviço de Autenticação e Bootstrap do Admin
	var authSvc *auth.Service
	if pg != nil && pg.Pool != nil {
		authSvc = auth.NewService(pg.Pool, rdb)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := authSvc.EnsureAdminUser(ctx, cfg.AdminUser, cfg.AdminPassword); err != nil {
			log.Printf("[Auth] Erro ao provisionar admin: %v", err)
		}
		cancel()
	}

	// Middleware de autenticação fail-closed
	var authMiddleware fiber.Handler
	if authSvc != nil {
		authMiddleware = authSvc.RequireAuth()
	} else {
		authMiddleware = func(c *fiber.Ctx) error {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"error": "serviço de autenticação temporariamente indisponível",
			})
		}
	}

	// 5. Inicializa o Handler do Coletor e de Integrações
	handler := collector.NewHandler(cfg, rdb, pg, ch)
	integHandler := integhandler.NewIntegrationsHandler(cfg, pg, rdb)
	integHandler.SetOnKeyRevoked(func(key string) {
		handler.InvalidateKey(key)
	})

	// 6. Inicializa o app Fiber de ultra-performance
	app := fiber.New(fiber.Config{
		ServerHeader:          "HN-Tracking-Engine",
		DisableStartupMessage: false,
	})

	// Middlewares globais defensivos
	app.Use(recover.New())
	app.Use(helmet.New(helmet.Config{
		XSSProtection:      "1; mode=block",
		ContentTypeNosniff: "nosniff",
		XFrameOptions:      "DENY",
		ReferrerPolicy:     "strict-origin-when-cross-origin",
	}))
	if cfg.Env == "development" {
		app.Use(logger.New())
	}

	// CORS Segregado: Ingestão de eventos e SDK são públicos
	ingestionCors := cors.New(cors.Config{
		AllowOrigins: "*",
		AllowMethods: "GET,POST,OPTIONS",
		AllowHeaders: "Origin,Content-Type,Accept,X-Site-Key,X-Server-Key",
	})
	app.Use("/api/v1/collect", ingestionCors)
	app.Use("/t", ingestionCors)
	app.Use("/sdk", ingestionCors)

	// CORS Segregado: Rotas administrativas restritas a hostnames estritamente autorizados
	adminCors := cors.New(cors.Config{
		AllowOriginsFunc: func(origin string) bool {
			if origin == "" {
				return true // Mesma origem ou navegação direta
			}
			u, err := url.Parse(origin)
			if err != nil {
				return false
			}
			host := strings.ToLower(u.Hostname())
			if host == "trackeamento.hnperformancedigital.com.br" {
				return true
			}
			if cfg.TrackingDomain != "" && host == strings.ToLower(cfg.TrackingDomain) {
				return true
			}
			if cfg.Env == "development" && (host == "localhost" || host == "127.0.0.1" || strings.HasSuffix(host, ".local")) {
				return true
			}
			return false
		},
		AllowMethods:     "GET,POST,PUT,DELETE,OPTIONS",
		AllowHeaders:     "Origin,Content-Type,Accept,X-Site-Key,Authorization",
		AllowCredentials: true,
	})
	app.Use("/api/v1/auth", adminCors)
	app.Use("/api/v1/domains", adminCors)
	app.Use("/api/v1/sites", adminCors)
	app.Use("/api/v1/alerts", adminCors)
	app.Use("/api/v1/security", adminCors)
	app.Use("/api/v1/analytics", adminCors)
	app.Use("/api/v1/debug", adminCors)

	// Landing page de status e documentação na raiz
	app.Get("/", func(c *fiber.Ctx) error {
		c.Set("Content-Type", "text/html; charset=utf-8")
		return c.Send(landingHTML)
	})

	// Dashboard dedicado de Analytics & Relatórios (Protegido por Autenticação)
	app.Get("/dashboard", func(c *fiber.Ctx) error {
		c.Set("Content-Type", "text/html; charset=utf-8")
		return c.Send(dashboardHTML)
	})

	// Guia de Instalação e Documentação Técnica do SDK (Público para Desenvolvedores)
	app.Get("/docs", func(c *fiber.Ctx) error {
		c.Set("Content-Type", "text/html; charset=utf-8")
		return c.Send(docsHTML)
	})
	app.Get("/setup", func(c *fiber.Ctx) error {
		return c.Redirect("/docs")
	})

	// Health check
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusOK).JSON(fiber.Map{
			"status":  "healthy",
			"service": "tracking-api",
		})
	})

	// Endpoints de Coleta Públicos
	app.Post("/api/v1/collect", handler.HandleCollect)
	app.Post("/t", handler.HandleCollect)

	// Rate Limiter para Login (máximo 5 tentativas por minuto por IP)
	loginLimiter := limiter.New(limiter.Config{
		Max:        5,
		Expiration: 1 * time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string {
			return c.IP()
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error": "Muitas tentativas de login. Aguarde 1 minuto para tentar novamente.",
			})
		},
	})

	// Endpoints de Autenticação
	if authSvc != nil {
		app.Post("/api/v1/auth/login", loginLimiter, authSvc.HandleLogin)
		app.Post("/api/v1/auth/logout", authSvc.HandleLogout)
		app.Get("/api/v1/auth/me", authSvc.HandleMe)
		app.Post("/api/v1/auth/users", authSvc.RequireAuth(), authSvc.HandleCreateUser)
	}

	// Endpoints de Alertas e Telemetria (Requer Autenticação)
	app.Get("/api/v1/alerts", authMiddleware, handler.HandleListAlerts)
	app.Get("/api/v1/security/bot-stats", authMiddleware, handler.HandleGetBotStats)

	// Endpoints de Gestão de Domínios e Sites (Requer Autenticação)
	app.Get("/api/v1/sites", authMiddleware, handler.HandleListSites)
	app.Post("/api/v1/sites", authMiddleware, handler.HandleCreateSite)
	app.Get("/api/v1/domains", authMiddleware, handler.HandleListDomains)
	app.Post("/api/v1/domains", authMiddleware, handler.HandleAddDomain)
	app.Delete("/api/v1/domains/:id", authMiddleware, handler.HandleDeleteDomain)
	app.Post("/api/v1/domains/approve", authMiddleware, handler.HandleApproveDomain)

	// Endpoints Analíticos (Requer Autenticação)
	app.Get("/api/v1/analytics/overview", authMiddleware, handler.HandleAnalyticsOverview)
	app.Get("/api/v1/analytics/pages", authMiddleware, handler.HandleAnalyticsPages)
	app.Get("/api/v1/analytics/leads", authMiddleware, handler.HandleAnalyticsLeads)
	app.Get("/api/v1/analytics/leads/export", authMiddleware, handler.HandleExportLeadsCSV)
	app.Get("/api/v1/analytics/funnel", authMiddleware, handler.HandleAnalyticsFunnel)
	app.Get("/api/v1/analytics/attribution/paths", authMiddleware, handler.HandleAnalyticsAttributionPaths)
	app.Get("/api/v1/analytics/visitor/journey", authMiddleware, handler.HandleAnalyticsVisitorJourney)

	// Endpoints de Depuração e DebugView em Tempo Real (Requer Autenticação)
	app.Get("/api/v1/debug/stream", authMiddleware, handler.HandleDebugStream)
	app.Post("/api/v1/debug/simulate", authMiddleware, handler.HandleDebugSimulate)
	app.Post("/api/v1/debug/clear", authMiddleware, handler.HandleDebugClear)

	// Endpoints de Integrações e Envios Server-Side / CAPI (Requer Autenticação)
	app.Get("/api/v1/sites/:site_id/integrations", authMiddleware, integHandler.HandleGetIntegrations)
	app.Put("/api/v1/sites/:site_id/integrations/:platform", authMiddleware, integHandler.HandleSaveIntegration)
	app.Post("/api/v1/sites/:site_id/integrations/:platform/test", authMiddleware, integHandler.HandleTestIntegration)

	// Endpoints de Gestão de Chaves de API por Site (Requer Autenticação)
	app.Get("/api/v1/sites/:site_id/keys", authMiddleware, integHandler.HandleListSiteKeys)
	app.Post("/api/v1/sites/:site_id/keys", authMiddleware, integHandler.HandleCreateSiteKey)
	app.Post("/api/v1/sites/:site_id/keys/:key_id/revoke", authMiddleware, integHandler.HandleRevokeSiteKey)

	// Endpoint para servir o SDK JS do Tracker
	app.Get("/sdk/tracker.js", func(c *fiber.Ctx) error {
		c.Set("Content-Type", "application/javascript; charset=utf-8")
		c.Set("Cache-Control", "public, max-age=3600")
		return c.SendFile("./sdk/tracker.js")
	})

	// Graceful Shutdown
	go func() {
		port := ":" + cfg.HTTPPort
		log.Printf("==> Tracking API iniciada na porta %s (Ambiente: %s)", port, cfg.Env)
		if err := app.Listen(port); err != nil {
			log.Fatalf("Erro no servidor HTTP: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Encerrando Tracking API graciosamente...")
	_ = app.Shutdown()
}
