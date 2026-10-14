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
	db.EnsureSitesCanonicalColumn(ctx)
	db.EnsureSitePageCatalogTable(ctx)
	db.EnsureSiteAPIKeysTable(ctx)
	db.EnsureCRMInboundEventsTable(ctx)
	db.EnsureServerAPIKeysTable(ctx)
	return db, nil
}

func (p *PostgresDB) EnsureCRMInboundEventsTable(ctx context.Context) {
	if p.Pool == nil {
		return
	}
	schema := `
	CREATE TABLE IF NOT EXISTS crm_inbound_events (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		site_id UUID NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
		provider VARCHAR(50) NOT NULL,
		event_name VARCHAR(100) NOT NULL DEFAULT 'purchase',
		status VARCHAR(50) NOT NULL DEFAULT 'pending',
		matched_visitor_id VARCHAR(100),
		contact_email VARCHAR(255),
		contact_phone VARCHAR(50),
		value NUMERIC(12,2) DEFAULT 0.00,
		currency VARCHAR(10) DEFAULT 'BRL',
		order_id VARCHAR(100),
		payload JSONB NOT NULL DEFAULT '{}'::jsonb,
		error_message TEXT,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		processed_at TIMESTAMPTZ
	);

	ALTER TABLE crm_inbound_events ADD COLUMN IF NOT EXISTS dedup_key VARCHAR(128);
	ALTER TABLE crm_inbound_events ADD COLUMN IF NOT EXISTS attempts INT NOT NULL DEFAULT 0;
	ALTER TABLE crm_inbound_events ADD COLUMN IF NOT EXISTS max_attempts INT NOT NULL DEFAULT 3;
	ALTER TABLE crm_inbound_events ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now();
	ALTER TABLE crm_inbound_events ADD COLUMN IF NOT EXISTS locked_at TIMESTAMPTZ;
	ALTER TABLE crm_inbound_events ADD COLUMN IF NOT EXISTS lease_expires_at TIMESTAMPTZ;
	ALTER TABLE crm_inbound_events ADD COLUMN IF NOT EXISTS occurred_at TIMESTAMPTZ NOT NULL DEFAULT now();
	ALTER TABLE crm_inbound_events ADD COLUMN IF NOT EXISTS value_cents BIGINT NOT NULL DEFAULT 0;
	ALTER TABLE crm_inbound_events ADD COLUMN IF NOT EXISTS contact_email_hash VARCHAR(64);
	ALTER TABLE crm_inbound_events ADD COLUMN IF NOT EXISTS contact_phone_hash VARCHAR(64);
	ALTER TABLE crm_inbound_events ADD COLUMN IF NOT EXISTS contact_phone_blind_index VARCHAR(64);
	ALTER TABLE crm_inbound_events ADD COLUMN IF NOT EXISTS gclid VARCHAR(128);
	ALTER TABLE crm_inbound_events ADD COLUMN IF NOT EXISTS fbclid VARCHAR(128);
	ALTER TABLE crm_inbound_events ADD COLUMN IF NOT EXISTS fbp VARCHAR(128);
	ALTER TABLE crm_inbound_events ADD COLUMN IF NOT EXISTS fbc VARCHAR(128);
	ALTER TABLE crm_inbound_events ADD COLUMN IF NOT EXISTS ttclid VARCHAR(128);
	ALTER TABLE crm_inbound_events ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '30 days');

	UPDATE crm_inbound_events
	SET dedup_key = COALESCE(
		NULLIF(order_id, '') || '_' || event_name,
		'legacy_' || id::text
	)
	WHERE dedup_key IS NULL;

	UPDATE crm_inbound_events
	SET value_cents = ROUND(value * 100)::bigint
	WHERE value_cents = 0 AND value > 0;

	DO $$
	BEGIN
		ALTER TABLE crm_inbound_events ALTER COLUMN dedup_key SET NOT NULL;
	EXCEPTION WHEN OTHERS THEN
		NULL;
	END $$;

	CREATE INDEX IF NOT EXISTS idx_crm_inbound_events_site_id ON crm_inbound_events(site_id);
	CREATE INDEX IF NOT EXISTS idx_crm_inbound_events_created_at ON crm_inbound_events(created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_crm_inbound_events_status ON crm_inbound_events(status);
	CREATE INDEX IF NOT EXISTS idx_crm_inbound_events_order ON crm_inbound_events(site_id, provider, order_id);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_crm_inbound_events_dedup ON crm_inbound_events(site_id, dedup_key);
	CREATE INDEX IF NOT EXISTS idx_crm_inbound_events_worker ON crm_inbound_events(status, next_attempt_at) WHERE status IN ('pending', 'processing');
	CREATE INDEX IF NOT EXISTS idx_crm_inbound_events_lease ON crm_inbound_events(status, lease_expires_at) WHERE status = 'processing';
	CREATE INDEX IF NOT EXISTS idx_crm_inbound_events_expires_at ON crm_inbound_events(expires_at);
	`
	_, _ = p.Pool.Exec(ctx, schema)
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

	UPDATE site_api_keys SET name = 'Chave de Acesso' WHERE name IS NULL OR TRIM(name) = '';
	UPDATE site_api_keys SET created_by = 'sistema' WHERE created_by IS NULL OR TRIM(created_by) = '';
	ALTER TABLE site_api_keys ALTER COLUMN name SET NOT NULL;
	ALTER TABLE site_api_keys ALTER COLUMN name SET DEFAULT 'Chave de Acesso';
	ALTER TABLE site_api_keys ALTER COLUMN created_by SET NOT NULL;
	ALTER TABLE site_api_keys ALTER COLUMN created_by SET DEFAULT 'sistema';

	INSERT INTO site_api_keys (site_id, key, name, status, created_by, created_at, updated_at)
	SELECT id, api_key, 'Chave Primária (Legada)', 'active', 'sistema', created_at, updated_at
	FROM sites
	WHERE api_key IS NOT NULL AND api_key != ''
	ON CONFLICT (key) DO NOTHING;
	`
	_, _ = p.Pool.Exec(ctx, schema)
}

func (p *PostgresDB) EnsureServerAPIKeysTable(ctx context.Context) {
	if p.Pool == nil {
		return
	}
	schema := `
	CREATE TABLE IF NOT EXISTS server_api_keys (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		site_id UUID NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
		key_hash VARCHAR(64) UNIQUE NOT NULL,
		key_prefix VARCHAR(16) NOT NULL,
		name VARCHAR(100) NOT NULL DEFAULT 'Chave de Servidor',
		scopes TEXT[] NOT NULL DEFAULT '{"crm:inbound","erp:conversions"}',
		status VARCHAR(20) NOT NULL DEFAULT 'active',
		created_by VARCHAR(100) NOT NULL DEFAULT 'sistema',
		revoked_by VARCHAR(100),
		revoked_at TIMESTAMPTZ,
		last_used_at TIMESTAMPTZ,
		total_requests_count BIGINT NOT NULL DEFAULT 0,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	);
	CREATE INDEX IF NOT EXISTS idx_server_api_keys_hash ON server_api_keys(key_hash);
	CREATE INDEX IF NOT EXISTS idx_server_api_keys_site_id ON server_api_keys(site_id);
	CREATE INDEX IF NOT EXISTS idx_server_api_keys_status ON server_api_keys(status);
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

// EnsureSitesCanonicalColumn assegura idempotentemente a presença da coluna canonical_settings na tabela sites
func (p *PostgresDB) EnsureSitesCanonicalColumn(ctx context.Context) {
	if p.Pool == nil {
		return
	}
	_, err := p.Pool.Exec(ctx, `
		ALTER TABLE sites ADD COLUMN IF NOT EXISTS canonical_settings JSONB NOT NULL DEFAULT '{}'::jsonb;
	`)
	if err != nil {
		log.Printf("[POSTGRES] Aviso: falha ao garantir coluna canonical_settings em sites: %v", err)
	}
}

// EnsureSitePageCatalogTable assegura idempotentemente a tabela site_page_catalog e colunas de sitemap em sites
func (p *PostgresDB) EnsureSitePageCatalogTable(ctx context.Context) {
	if p.Pool == nil {
		return
	}
	schema := `
	CREATE TABLE IF NOT EXISTS site_page_catalog (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		site_id UUID NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
		page_key TEXT NOT NULL,
		canonical_url TEXT NOT NULL,
		title VARCHAR(500) DEFAULT '',
		in_sitemap BOOLEAN NOT NULL DEFAULT false,
		source VARCHAR(50) NOT NULL DEFAULT 'traffic',
		status VARCHAR(50) NOT NULL DEFAULT 'monitored',
		last_seen_in_sitemap_at TIMESTAMPTZ,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		CONSTRAINT uq_site_page_catalog UNIQUE (site_id, page_key)
	);

	CREATE INDEX IF NOT EXISTS idx_site_page_catalog_site_id ON site_page_catalog(site_id);
	CREATE INDEX IF NOT EXISTS idx_site_page_catalog_in_sitemap ON site_page_catalog(site_id, in_sitemap);
	CREATE INDEX IF NOT EXISTS idx_site_page_catalog_page_key ON site_page_catalog(site_id, page_key);

	ALTER TABLE sites ADD COLUMN IF NOT EXISTS sitemap_url VARCHAR(500);
	ALTER TABLE sites ADD COLUMN IF NOT EXISTS last_sitemap_sync_at TIMESTAMPTZ;
	ALTER TABLE sites ADD COLUMN IF NOT EXISTS sitemap_status VARCHAR(50) DEFAULT 'idle';
	ALTER TABLE sites ADD COLUMN IF NOT EXISTS sitemap_total_urls INT DEFAULT 0;
	ALTER TABLE sites ADD COLUMN IF NOT EXISTS last_sitemap_error TEXT;
	`
	_, err := p.Pool.Exec(ctx, schema)
	if err != nil {
		log.Printf("[POSTGRES] Aviso: falha ao garantir tabela site_page_catalog e colunas de sitemap: %v", err)
	}
}



