-- Migration 003: Tabelas de Domínios Permitidos e Auditoria de Alertas de Segurança

-- 1. Tabela de Domínios Permitidos por Site
CREATE TABLE IF NOT EXISTS site_allowed_domains (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    site_id UUID NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    domain VARCHAR(255) NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_site_domain UNIQUE (site_id, domain)
);

CREATE INDEX IF NOT EXISTS idx_site_allowed_domains_site ON site_allowed_domains(site_id);

-- 2. Tabela de Alertas de Segurança para Domínios Não Autorizados
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

-- 3. Migração automática: Popular site_allowed_domains com os domínios já existentes na tabela sites
INSERT INTO site_allowed_domains (site_id, domain)
SELECT id, LOWER(TRIM(domain))
FROM sites
WHERE domain IS NOT NULL AND TRIM(domain) != ''
ON CONFLICT (site_id, domain) DO NOTHING;
