package storage

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"tracking-engine/internal/config"
	"tracking-engine/internal/prefixedid"
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

// BootstrapDeclarativeSite provisiona opcionalmente o site inicial se SEED_DEFAULT_SITE=true e o banco estiver com 0 sites
func (p *PostgresDB) BootstrapDeclarativeSite(ctx context.Context, cfg *config.Config) error {
	if !cfg.SeedDefaultSite {
		return nil
	}
	if p.Pool == nil {
		return nil
	}

	// 1. Checa idempotência: só executa se a tabela de sites estiver totalmente vazia
	var siteCount int
	err := p.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM sites").Scan(&siteCount)
	if err != nil {
		return fmt.Errorf("falha ao verificar contagem de sites para bootstrap: %w", err)
	}
	if siteCount > 0 {
		// Banco já possui sites; não sobrescreve nem duplica
		return nil
	}

	// 2. Prepara parâmetros declarados no .env
	clientName := strings.TrimSpace(cfg.DefaultClientName)
	if clientName == "" {
		clientName = "Minha Organização"
	}
	siteName := strings.TrimSpace(cfg.DefaultSiteName)
	if siteName == "" {
		siteName = "Meu Site Principal"
	}
	siteDomain := strings.TrimSpace(cfg.DefaultSiteDomain)
	if siteDomain == "" {
		siteDomain = strings.TrimSpace(cfg.TrackingDomain)
	}
	if siteDomain == "" {
		siteDomain = "localhost"
	}

	// 3. Cria cliente/organização
	var clientID string
	err = p.Pool.QueryRow(ctx, `
		INSERT INTO clients (name, created_at, updated_at)
		VALUES ($1, now(), now())
		RETURNING id::text
	`, clientName).Scan(&clientID)
	if err != nil {
		return fmt.Errorf("falha ao criar cliente no bootstrap: %w", err)
	}

	// 4. Cria site inicial com chave padronizada
	siteKey := prefixedid.GenerateSiteKey()
	var siteID string
	err = p.Pool.QueryRow(ctx, `
		INSERT INTO sites (client_id, domain, name, api_key, is_active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, true, now(), now())
		RETURNING id::text
	`, clientID, siteDomain, siteName, siteKey).Scan(&siteID)
	if err != nil {
		return fmt.Errorf("falha ao criar site no bootstrap: %w", err)
	}

	// 5. Adiciona domínio permitido na whitelist
	_, _ = p.Pool.Exec(ctx, `
		INSERT INTO site_allowed_domains (site_id, domain, is_active)
		VALUES ($1, $2, true)
		ON CONFLICT (site_id, domain) DO NOTHING
	`, siteID, siteDomain)

	// 6. Registra na tabela de chaves de API
	_, _ = p.Pool.Exec(ctx, `
		INSERT INTO site_api_keys (site_id, key, name, status, created_by, created_at, updated_at)
		VALUES ($1, $2, 'Chave Inicial (Bootstrap)', 'active', 'system', now(), now())
		ON CONFLICT (key) DO NOTHING
	`, siteID, siteKey)

	log.Printf("[BOOTSTRAP] ✅ Site inicial provisionado com sucesso: %s (%s) | Chave: %s", siteName, siteDomain, siteKey)
	return nil
}


