-- Migration 013: Resiliência, Dedup Atômico, Worker com Lease e Privacidade para Webhooks Inbound
-- Adiciona campos estruturais na tabela crm_inbound_events com backfill para integridade

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

-- Backfill defensivo de linhas legadas antes de aplicar restrição NOT NULL e índice único
UPDATE crm_inbound_events
SET dedup_key = COALESCE(
    NULLIF(order_id, '') || '_' || event_name,
    'legacy_' || id::text
)
WHERE dedup_key IS NULL;

UPDATE crm_inbound_events
SET value_cents = ROUND(value * 100)::bigint
WHERE value_cents = 0 AND value > 0;

ALTER TABLE crm_inbound_events ALTER COLUMN dedup_key SET NOT NULL;

-- Índices otimizados para concorrência de worker e deduplicação
CREATE UNIQUE INDEX IF NOT EXISTS idx_crm_inbound_events_dedup ON crm_inbound_events(site_id, dedup_key);
CREATE INDEX IF NOT EXISTS idx_crm_inbound_events_worker ON crm_inbound_events(status, next_attempt_at) WHERE status IN ('pending', 'processing');
CREATE INDEX IF NOT EXISTS idx_crm_inbound_events_lease ON crm_inbound_events(status, lease_expires_at) WHERE status = 'processing';
CREATE INDEX IF NOT EXISTS idx_crm_inbound_events_expires_at ON crm_inbound_events(expires_at);
