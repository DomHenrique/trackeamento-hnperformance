package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"tracking-engine/internal/config"
	"tracking-engine/internal/identity"
	"tracking-engine/internal/ingester"
	"tracking-engine/internal/storage"
)

func main() {
	cfg := config.Load()
	log.Println("==> Inicializando ClickHouse Batch Ingester...")

	// 1. Conexão Redis
	rdb, err := storage.NewRedis(cfg)
	if err != nil {
		log.Fatalf("Erro ao conectar ao Redis: %v", err)
	}
	defer rdb.Close()

	// 2. Conexão ClickHouse
	ch, err := storage.NewClickHouse(cfg)
	if err != nil {
		log.Fatalf("Erro ao conectar ao ClickHouse: %v", err)
	}
	defer ch.Close()

	// 3. Conexão Postgres para Grafo de Identidade (opcional/graceful)
	pg, err := storage.NewPostgres(cfg)
	if err != nil {
		log.Printf("Aviso: Falha ao conectar ao Postgres no Ingester: %v (seguindo sem reconciliação)", err)
	} else {
		defer pg.Close()
	}

	identService := identity.NewService(pg, rdb)

	// 4. Inicializa e roda o Worker
	worker := ingester.NewWorker(cfg, rdb, ch, identService)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-quit
		log.Println("Encerrando Ingester graciosamente...")
		cancel()
	}()

	if err := worker.Start(ctx); err != nil && err != context.Canceled {
		log.Fatalf("Erro na execução do Ingester: %v", err)
	}
	log.Println("Ingester finalizado.")
}
