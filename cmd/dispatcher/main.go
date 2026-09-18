package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"tracking-engine/internal/config"
	"tracking-engine/internal/dispatcher"
	"tracking-engine/internal/storage"
)

func main() {
	cfg := config.Load()
	log.Println("==> Inicializando Server-Side Dispatcher...")

	// 1. Conexão Redis
	rdb, err := storage.NewRedis(cfg)
	if err != nil {
		log.Fatalf("Erro ao conectar ao Redis no Dispatcher: %v", err)
	}
	defer rdb.Close()

	// 2. Conexão Postgres (para consulta de credenciais de integração)
	pg, err := storage.NewPostgres(cfg)
	if err != nil {
		log.Printf("Aviso: Falha ao conectar ao Postgres no Dispatcher: %v (seguindo sem integrações de banco)", err)
	} else {
		defer pg.Close()
	}

	// 3. Inicializa WorkerPool do Dispatcher
	pool := dispatcher.NewWorkerPool(cfg, rdb, pg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-quit
		log.Println("Encerrando Dispatcher graciosamente...")
		cancel()
	}()

	if err := pool.Start(ctx); err != nil && err != context.Canceled {
		log.Fatalf("Erro na execução do Dispatcher: %v", err)
	}
	log.Println("Dispatcher finalizado.")
}
