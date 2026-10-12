-- Migration 010: Tabela de Auditoria e Ingestão de Webhooks Inbound de CRM e ERP
CREATE TABLE IF NOT EXISTS crm_inbound_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    site_id UUID NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    provider VARCHAR(50) NOT NULL, -- 'rdstation', 'hubspot', 'pipedrive', 'activecampaign', 'omie', 'asaas', 'kiwify', 'custom'
    event_name VARCHAR(100) NOT NULL DEFAULT 'purchase',
    status VARCHAR(50) NOT NULL DEFAULT 'pending', -- 'matched', 'unmatched', 'duplicate', 'error'
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

CREATE INDEX IF NOT EXISTS idx_crm_inbound_events_site_id ON crm_inbound_events(site_id);
CREATE INDEX IF NOT EXISTS idx_crm_inbound_events_created_at ON crm_inbound_events(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_crm_inbound_events_status ON crm_inbound_events(status);
CREATE INDEX IF NOT EXISTS idx_crm_inbound_events_order ON crm_inbound_events(site_id, provider, order_id);
