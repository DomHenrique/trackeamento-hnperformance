-- Ativa extensão para geração de UUID
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- Tabela de Clientes / Organizações (Multitenant)
CREATE TABLE IF NOT EXISTS clients (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(255) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Tabela de Sites / Propriedades monitoradas
CREATE TABLE IF NOT EXISTS sites (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id UUID NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    domain VARCHAR(255) NOT NULL,
    name VARCHAR(255) NOT NULL,
    api_key VARCHAR(64) UNIQUE NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT true,
    privacy_settings JSONB NOT NULL DEFAULT '{"version":"1.0","enforce_gpc":true,"mask_ip":true,"categories_policy":{"necessary":{"requires_consent":false,"mask_ip_mode":"last_octet"},"analytics":{"requires_consent":true,"issue_visitor_cookie":true,"cookie_lifespan_days":365},"marketing":{"requires_consent":true,"persist_attribution_params":true,"allow_third_party_dispatch":true}},"retention_policy_days":{"raw_events_clickhouse":90,"aggregated_events_clickhouse":730,"inactive_visitors_postgres":365}}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sites_api_key ON sites(api_key);
CREATE INDEX IF NOT EXISTS idx_sites_client_id ON sites(client_id);
CREATE INDEX IF NOT EXISTS idx_sites_privacy_settings ON sites USING gin (privacy_settings);

-- Tabela de Configurações de Integração Server-Side
CREATE TABLE IF NOT EXISTS site_integrations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    site_id UUID NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    platform VARCHAR(50) NOT NULL, -- 'meta_capi', 'google_ads', 'ga4', 'webhook'
    is_active BOOLEAN NOT NULL DEFAULT true,
    credentials JSONB NOT NULL DEFAULT '{}'::jsonb, -- pixel_id, access_token, api_secret, webhook_url
    event_mappings JSONB NOT NULL DEFAULT '{}'::jsonb, -- mapeamento de nomes de evento
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_site_platform UNIQUE (site_id, platform)
);

CREATE INDEX IF NOT EXISTS idx_site_integrations_site ON site_integrations(site_id);

-- Tabela de Visitantes e Atribuição de Primeiro Contato (First-Touch)
CREATE TABLE IF NOT EXISTS visitors (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    site_id UUID NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    visitor_id VARCHAR(64) NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    
    -- Atribuição First-Touch (Imutável após primeira visita)
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
    
    -- Dados de Identidade Normalizados (Hash SHA-256 para LGPD e Meta CAPI)
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
