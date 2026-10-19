-- Migration 004: Colunas de Atribuição Canônica Derivada e Herança de Sessão
ALTER TABLE tracking_events.events
    ADD COLUMN IF NOT EXISTS source LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS medium LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS channel LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS attribution_basis LowCardinality(String) DEFAULT 'none',
    ADD COLUMN IF NOT EXISTS inherited UInt8 DEFAULT 0;
