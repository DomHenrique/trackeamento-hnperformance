package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"tracking-engine/internal/config"
)

type ClickHouseDB struct {
	Conn driver.Conn
}

func NewClickHouse(cfg *config.Config) (*ClickHouseDB, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{cfg.ClickHouseAddr},
		Auth: clickhouse.Auth{
			Database: cfg.ClickHouseDB,
			Username: cfg.ClickHouseUser,
			Password: cfg.ClickHousePassword,
		},
		Settings: clickhouse.Settings{
			"max_execution_time": 60,
		},
		DialTimeout: 5 * time.Second,
		Compression: &clickhouse.Compression{
			Method: clickhouse.CompressionLZ4,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("erro ao abrir conexao com clickhouse: %w", err)
	}

	if err := conn.Ping(ctx); err != nil {
		// Loga aviso em vez de travar caso o container ainda esteja iniciando
		fmt.Printf("Aviso: ping no ClickHouse falhou (%v). O serviço continuará tentando.\n", err)
	}

	return &ClickHouseDB{Conn: conn}, nil
}

func (c *ClickHouseDB) Close() error {
	if c.Conn != nil {
		return c.Conn.Close()
	}
	return nil
}
