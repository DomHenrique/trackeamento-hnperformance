package main

import (
	_ "embed"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"

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

	// 3. Inicializa o Handler do Coletor
	handler := collector.NewHandler(cfg, rdb, pg)

	// 4. Inicializa o app Fiber de ultra-performance
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
		AllowMethods:     "GET,POST,OPTIONS",
		AllowHeaders:     "Origin,Content-Type,Accept,X-Site-Key",
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
