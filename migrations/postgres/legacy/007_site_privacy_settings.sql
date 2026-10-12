-- Migração 007: Adiciona coluna privacy_settings na tabela sites para governança de privacidade e conformidade LGPD
ALTER TABLE sites 
ADD COLUMN IF NOT EXISTS privacy_settings JSONB NOT NULL DEFAULT '{
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

-- Cria índice GIN para consultas rápidas sobre flags de privacidade caso necessário
CREATE INDEX IF NOT EXISTS idx_sites_privacy_settings ON sites USING gin (privacy_settings);
