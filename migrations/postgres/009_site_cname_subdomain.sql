-- Migration 009: Subdomínio CNAME First-Party Gateway por Site

-- 1. Adiciona colunas para subdomínio CNAME personalizado e status de verificação DNS
ALTER TABLE sites ADD COLUMN IF NOT EXISTS cname_subdomain VARCHAR(255);
ALTER TABLE sites ADD COLUMN IF NOT EXISTS cname_verified BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE sites ADD COLUMN IF NOT EXISTS cname_verified_at TIMESTAMPTZ;

-- 2. Índice para resolução ultra-rápida de site pelo cabeçalho Host
CREATE INDEX IF NOT EXISTS idx_sites_cname_subdomain ON sites(LOWER(cname_subdomain)) WHERE cname_subdomain IS NOT NULL;
