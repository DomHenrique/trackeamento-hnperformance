package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"tracking-engine/internal/config"
	"tracking-engine/internal/crypto"
	"tracking-engine/internal/storage"
)

func main() {
	rekeyFlag := flag.Bool("rekey", false, "Recifra credenciais cifradas sob kids antigos com a chave atual (CREDENTIALS_CURRENT_KEY)")
	dryRunFlag := flag.Bool("dry-run", false, "Simula a operação sem gravar alterações no banco de dados")
	timeoutSec := flag.Int("timeout", 60, "Tempo limite em segundos para a operação de migração")
	flag.Parse()

	log.Println("==> Utilitário de Migração e Rotação de Credenciais (OpenSpec PR B)")

	cfg := config.Load()
	if cfg.KeyRing == nil {
		log.Fatalf("[FATAL] KeyRing de credenciais não configurado. Certifique-se de definir CREDENTIALS_KEYS e CREDENTIALS_CURRENT_KEY.")
	}

	log.Printf("Chave ativa corrente (CREDENTIALS_CURRENT_KEY): %s", cfg.KeyRing.CurrentKID())
	if *rekeyFlag {
		log.Println("Modo --rekey ATIVADO: registros com chaves antigas serão rotacionados para a chave ativa.")
	}
	if *dryRunFlag {
		log.Println("Modo --dry-run ATIVADO: nenhuma alteração será persistida no banco.")
	}

	pg, err := storage.NewPostgres(cfg)
	if err != nil {
		log.Fatalf("[FATAL] Falha ao conectar ao PostgreSQL: %v", err)
	}
	defer pg.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSec)*time.Second)
	defer cancel()

	startTime := time.Now()
	stats, err := crypto.RunRekeyMigration(ctx, pg.Pool, cfg.KeyRing, *rekeyFlag, *dryRunFlag)
	if err != nil {
		log.Fatalf("[FATAL] Falha durante a migração de credenciais: %v", err)
	}

	duration := time.Since(startTime)
	fmt.Println("\n==================================================")
	fmt.Println(" Relatório de Migração de Credenciais")
	fmt.Println("==================================================")
	fmt.Printf("Tempo de Execução:        %v\n", duration)
	fmt.Printf("Total de Registros Lidos: %d\n", stats.TotalProcessed)
	fmt.Printf("Registros Atualizados:    %d\n", stats.UpdatedRecords)
	fmt.Printf("Campos Cifrados:          %d\n", stats.FieldsEncrypted)
	fmt.Printf("Campos Rotacionados:      %d\n", stats.FieldsRekeyed)
	fmt.Println("==================================================")

	if *dryRunFlag {
		fmt.Println("Simulação concluída com sucesso. Nenhuma alteração gravada.")
	} else {
		fmt.Println("Migração concluída com sucesso!")
	}
	os.Exit(0)
}
