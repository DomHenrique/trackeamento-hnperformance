-- ==============================================================================
-- HN Performance Tracking Engine - Unified Production Database Schema
-- Arquivo Mestre Unificado e Idempotente (Substitui migrations fragmentadas 001 a 013)
-- Suporta inicialização do zero ou atualização idempotente de bancos existentes.
-- ==============================================================================

-- 1. Extensões Essenciais
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- 2. Tabela de Clientes / Organizações (Multitenant)
CREATE TABLE IF NOT EXISTS clients (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(255) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 3. Tabela de Sites / Propriedades Monitoradas
CREATE TABLE IF NOT EXISTS sites (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id UUID NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    domain VARCHAR(255) NOT NULL,
    name VARCHAR(255) NOT NULL,
    api_key VARCHAR(64) UNIQUE NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT true,
    cname_subdomain VARCHAR(255),
    cname_verified BOOLEAN NOT NULL DEFAULT false,
    cname_verified_at TIMESTAMPTZ,
    privacy_settings JSONB NOT NULL DEFAULT '{
        "version": "1.0",
        "enforce_gpc": true,
        "mask_ip": true,
        "categories_policy": {
            "necessary": { "requires_consent": false, "mask_ip_mode": "last_octet" },
            "analytics": { "requires_consent": true, "issue_visitor_cookie": true, "cookie_lifespan_days": 365 },
            "marketing": { "requires_consent": true, "persist_attribution_params": true, "allow_third_party_dispatch": true }
        },
        "retention_policy_days": {
            "raw_events_clickhouse": 90,
            "aggregated_events_clickhouse": 730,
            "inactive_visitors_postgres": 365
        }
    }'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Atualizações idempotentes em sites para bancos já existentes
ALTER TABLE sites ADD COLUMN IF NOT EXISTS cname_subdomain VARCHAR(255);
ALTER TABLE sites ADD COLUMN IF NOT EXISTS cname_verified BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE sites ADD COLUMN IF NOT EXISTS cname_verified_at TIMESTAMPTZ;
ALTER TABLE sites ADD COLUMN IF NOT EXISTS privacy_settings JSONB NOT NULL DEFAULT '{
    "version": "1.0",
    "enforce_gpc": true,
    "mask_ip": true,
    "categories_policy": {
        "necessary": { "requires_consent": false, "mask_ip_mode": "last_octet" },
        "analytics": { "requires_consent": true, "issue_visitor_cookie": true, "cookie_lifespan_days": 365 },
        "marketing": { "requires_consent": true, "persist_attribution_params": true, "allow_third_party_dispatch": true }
    },
    "retention_policy_days": {
        "raw_events_clickhouse": 90,
        "aggregated_events_clickhouse": 730,
        "inactive_visitors_postgres": 365
    }
}'::jsonb;

CREATE INDEX IF NOT EXISTS idx_sites_api_key ON sites(api_key);
CREATE INDEX IF NOT EXISTS idx_sites_client_id ON sites(client_id);
CREATE INDEX IF NOT EXISTS idx_sites_privacy_settings ON sites USING gin (privacy_settings);
CREATE INDEX IF NOT EXISTS idx_sites_cname_subdomain ON sites(LOWER(cname_subdomain)) WHERE cname_subdomain IS NOT NULL;

-- 4. Domínios Autorizados por Site (Anti-Hijacking)
CREATE TABLE IF NOT EXISTS site_allowed_domains (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    site_id UUID NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    domain VARCHAR(255) NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_site_domain UNIQUE (site_id, domain)
);

CREATE INDEX IF NOT EXISTS idx_site_allowed_domains_site ON site_allowed_domains(site_id);

-- Auto-população defensiva com domínios cadastrados
INSERT INTO site_allowed_domains (site_id, domain)
SELECT id, LOWER(TRIM(domain))
FROM sites
WHERE domain IS NOT NULL AND TRIM(domain) != ''
ON CONFLICT (site_id, domain) DO NOTHING;

-- 5. Alertas de Segurança de Domínio
CREATE TABLE IF NOT EXISTS site_domain_alerts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    site_id UUID NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    unauthorized_domain VARCHAR(255) NOT NULL,
    attempts_count INT NOT NULL DEFAULT 1,
    first_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_ip VARCHAR(45),
    last_user_agent TEXT,
    last_raw_url TEXT,
    CONSTRAINT uq_site_unauthorized_domain UNIQUE (site_id, unauthorized_domain)
);

CREATE INDEX IF NOT EXISTS idx_site_domain_alerts_site ON site_domain_alerts(site_id);
CREATE INDEX IF NOT EXISTS idx_site_domain_alerts_last_attempt ON site_domain_alerts(last_attempt_at DESC);

-- 6. Configurações de Integrações Server-Side (Meta CAPI, Google Ads, GA4, Webhooks)
CREATE TABLE IF NOT EXISTS site_integrations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    site_id UUID NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    platform VARCHAR(50) NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT true,
    credentials JSONB NOT NULL DEFAULT '{}'::jsonb,
    event_mappings JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_site_platform UNIQUE (site_id, platform)
);

CREATE INDEX IF NOT EXISTS idx_site_integrations_site ON site_integrations(site_id);

-- 7. Tabela de Visitantes e Atribuição de Primeiro Contato (First-Touch)
CREATE TABLE IF NOT EXISTS visitors (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    site_id UUID NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    visitor_id VARCHAR(64) NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    first_landing_page TEXT,
    first_referrer TEXT,
    first_utm_source VARCHAR(100),
    first_utm_medium VARCHAR(100),
    first_utm_campaign VARCHAR(255),
    first_utm_content VARCHAR(255),
    first_utm_term VARCHAR(255),
    first_gclid VARCHAR(255),
    first_fbclid VARCHAR(255),
    first_ttclid VARCHAR(255),
    identified_name VARCHAR(255),
    identified_email_hash VARCHAR(64),
    identified_phone_hash VARCHAR(64),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_visitors_site_visitor UNIQUE (site_id, visitor_id)
);

CREATE INDEX IF NOT EXISTS idx_visitors_site_last_seen ON visitors(site_id, last_seen_at DESC);
CREATE INDEX IF NOT EXISTS idx_visitors_site_visitor_id ON visitors(site_id, visitor_id);
CREATE INDEX IF NOT EXISTS idx_visitors_email_hash ON visitors(identified_email_hash);
CREATE INDEX IF NOT EXISTS idx_visitors_phone_hash ON visitors(identified_phone_hash);
CREATE INDEX IF NOT EXISTS idx_visitors_gclid ON visitors(first_gclid);
CREATE INDEX IF NOT EXISTS idx_visitors_fbclid ON visitors(first_fbclid);
CREATE INDEX IF NOT EXISTS idx_visitors_retention ON visitors(site_id, last_seen_at);

-- 8. Tabela de Usuários Administrativos
CREATE TABLE IF NOT EXISTS app_users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username VARCHAR(100) UNIQUE NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    role VARCHAR(20) NOT NULL DEFAULT 'operator',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_app_users_username ON app_users(username);

-- 9. Chaves de Site (Client-Side Públicas / hn_site_)
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

-- Backfill defensivo e constraints para site_api_keys
UPDATE site_api_keys SET name = 'Chave de Acesso' WHERE name IS NULL OR TRIM(name) = '';
UPDATE site_api_keys SET created_by = 'sistema' WHERE created_by IS NULL OR TRIM(created_by) = '';

ALTER TABLE site_api_keys
    ALTER COLUMN name SET NOT NULL,
    ALTER COLUMN name SET DEFAULT 'Chave Padrão',
    ALTER COLUMN created_by SET NOT NULL,
    ALTER COLUMN created_by SET DEFAULT 'sistema';

CREATE INDEX IF NOT EXISTS idx_site_api_keys_key ON site_api_keys(key);
CREATE INDEX IF NOT EXISTS idx_site_api_keys_site_id ON site_api_keys(site_id);
CREATE INDEX IF NOT EXISTS idx_site_api_keys_status ON site_api_keys(status);

-- 10. Chaves de Servidor Privadas (Inbound de CRM/ERP / hn_sec_) com Hash SHA-256
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

-- 11. Ingestão Durável de Webhooks de CRM/ERP com Dedup Atômico e Worker com Lease
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
    value_cents BIGINT NOT NULL DEFAULT 0,
    currency VARCHAR(10) DEFAULT 'BRL',
    order_id VARCHAR(100),
    dedup_key VARCHAR(128),
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    error_message TEXT,
    attempts INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 3,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    locked_at TIMESTAMPTZ,
    lease_expires_at TIMESTAMPTZ,
    contact_email_hash VARCHAR(64),
    contact_phone_hash VARCHAR(64),
    contact_phone_blind_index VARCHAR(64),
    gclid VARCHAR(128),
    fbclid VARCHAR(128),
    fbp VARCHAR(128),
    fbc VARCHAR(128),
    ttclid VARCHAR(128),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '30 days'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at TIMESTAMPTZ
);

-- Atualizações idempotentes em crm_inbound_events para bancos já existentes
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

-- Backfill defensivo antes da restrição de unicidade
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
