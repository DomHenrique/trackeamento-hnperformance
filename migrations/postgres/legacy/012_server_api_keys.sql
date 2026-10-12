-- Migration 012: Tabela de Credenciais de Servidor e Inbound com Hash SHA-256
CREATE TABLE IF NOT EXISTS server_api_keys (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    site_id UUID NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    key_hash VARCHAR(64) UNIQUE NOT NULL,
    key_prefix VARCHAR(16) NOT NULL,
    name VARCHAR(100) NOT NULL DEFAULT 'Chave de Servidor',
    scopes TEXT[] NOT NULL DEFAULT '{"crm:inbound","erp:conversions"}',
    status VARCHAR(20) NOT NULL DEFAULT 'active', -- 'active', 'inactive', 'revoked'
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
