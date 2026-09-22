package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"tracking-engine/internal/config"
)

type PostgresDB struct {
	Pool *pgxpool.Pool
}

func NewPostgres(cfg *config.Config) (*PostgresDB, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s",
		cfg.PostgresUser, cfg.PostgresPassword, cfg.PostgresHost, cfg.PostgresPort, cfg.PostgresDB, cfg.PostgresSSLMode)

	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("erro ao configurar pool do postgres: %w", err)
	}

	poolConfig.MaxConns = 25
	poolConfig.MinConns = 2
	poolConfig.MaxConnLifetime = 1 * time.Hour
	poolConfig.MaxConnIdleTime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("erro ao inicializar pool postgres: %w", err)
	}

	db := &PostgresDB{Pool: pool}
	db.EnsureSiteAPIKeysTable(ctx)
	return db, nil
}

func (p *PostgresDB) EnsureSiteAPIKeysTable(ctx context.Context) {
	if p.Pool == nil {
		return
	}
	schema := `
	CREATE TABLE IF NOT EXISTS site_api_keys (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		site_id UUID NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
		key VARCHAR(64) UNIQUE NOT NULL,
		name VARCHAR(100) NOT NULL DEFAULT 'Chave Padrão',
		status VARCHAR(20) NOT NULL DEFAULT 'active',
		created_by VARCHAR(100) NOT NULL DEFAULT 'sistema',
		revoked_by VARCHAR(100),
		revoked_at TIMESTAMPTZ,
		last_used_at TIMESTAMPTZ,
		total_events_count BIGINT NOT NULL DEFAULT 0,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	);
	CREATE INDEX IF NOT EXISTS idx_site_api_keys_key ON site_api_keys(key);
	CREATE INDEX IF NOT EXISTS idx_site_api_keys_site_id ON site_api_keys(site_id);
	CREATE INDEX IF NOT EXISTS idx_site_api_keys_status ON site_api_keys(status);

	INSERT INTO site_api_keys (site_id, key, name, status, created_by, created_at, updated_at)
	SELECT id, api_key, 'Chave Primária (Legada)', 'active', 'sistema', created_at, updated_at
	FROM sites
	WHERE api_key IS NOT NULL AND api_key != ''
	ON CONFLICT (key) DO NOTHING;
	`
	_, _ = p.Pool.Exec(ctx, schema)
}

func (p *PostgresDB) Close() {
	if p.Pool != nil {
		p.Pool.Close()
	}
}

