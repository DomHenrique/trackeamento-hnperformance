-- Migration 005: Tabela de Histórico e Auditoria de Múltiplas Chaves de API por Site
CREATE TABLE IF NOT EXISTS site_api_keys (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    site_id UUID NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    key VARCHAR(64) UNIQUE NOT NULL,
    name VARCHAR(100) NOT NULL DEFAULT 'Chave Padrão',
    status VARCHAR(20) NOT NULL DEFAULT 'active', -- 'active', 'inactive', 'revoked'
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

-- Migração automática e retroativa: importa as chaves existentes em sites para site_api_keys se ainda não existirem
INSERT INTO site_api_keys (site_id, key, name, status, created_by, created_at, updated_at)
SELECT id, api_key, 'Chave Primária (Legada)', 'active', 'sistema', created_at, updated_at
FROM sites
WHERE api_key IS NOT NULL AND api_key != ''
ON CONFLICT (key) DO NOTHING;
