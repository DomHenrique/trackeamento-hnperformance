package main

import (
	_ "embed"
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"

	"tracking-engine/internal/auth"
	"tracking-engine/internal/collector"
	"tracking-engine/internal/config"
	"tracking-engine/internal/storage"
)

//go:embed landing.html
var landingHTML []byte

func main() {
	cfg := config.Load()

	// 1. Conexão com Redis (buffer de streaming)
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
		authSvc = auth.NewService(pg.Pool)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := authSvc.EnsureAdminUser(ctx, cfg.AdminUser, cfg.AdminPassword); err != nil {
			log.Printf("[Auth] Erro ao provisionar admin: %v", err)
		}
		cancel()
	}

	// 5. Inicializa o Handler do Coletor
	handler := collector.NewHandler(cfg, rdb, pg, ch)

	// 6. Inicializa o app Fiber de ultra-performance
	app := fiber.New(fiber.Config{
		ServerHeader:          "HN-Tracking-Engine",
		DisableStartupMessage: false,
	})

	// Middlewares
	app.Use(recover.New())
	app.Use(cors.New(cors.Config{
		AllowOriginsFunc: func(origin string) bool {
			return true
		},
		AllowMethods:     "GET,POST,DELETE,OPTIONS",
		AllowHeaders:     "Origin,Content-Type,Accept,X-Site-Key,Authorization",
		AllowCredentials: true,
	}))

	if cfg.Env == "development" {
		app.Use(logger.New())
	}

	// Landing page de status e documentação na raiz
	app.Get("/", func(c *fiber.Ctx) error {
		c.Set("Content-Type", "text/html; charset=utf-8")
		return c.Send(landingHTML)
	})

	// Health check
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusOK).JSON(fiber.Map{
			"status":  "healthy",
			"service": "tracking-api",
		})
	})

	// Endpoints de Coleta
	app.Post("/api/v1/collect", handler.HandleCollect)
	app.Post("/t", handler.HandleCollect)

	// Endpoint de Alertas de Segurança (Domínios não autorizados)
	app.Get("/api/v1/alerts", handler.HandleListAlerts)

	// Endpoint de Telemetria de Robôs e Navegações Suspeitas
	app.Get("/api/v1/security/bot-stats", handler.HandleGetBotStats)

	// Endpoints de Autenticação
	if authSvc != nil {
		app.Post("/api/v1/auth/login", authSvc.HandleLogin)
		app.Post("/api/v1/auth/logout", authSvc.HandleLogout)
		app.Get("/api/v1/auth/me", authSvc.HandleMe)
		app.Post("/api/v1/auth/users", authSvc.HandleCreateUser)
	}

	// Endpoints de Gestão de Domínios
	app.Get("/api/v1/sites", handler.HandleListSites)
	app.Get("/api/v1/domains", handler.HandleListDomains)
	app.Post("/api/v1/domains", handler.HandleAddDomain)
	app.Delete("/api/v1/domains/:id", handler.HandleDeleteDomain)
	app.Post("/api/v1/domains/approve", handler.HandleApproveDomain)

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
