-- Migration 002: Adicionar colunas de detecção e auditoria de robôs
ALTER TABLE tracking_events.events 
ADD COLUMN IF NOT EXISTS is_bot UInt8 DEFAULT 0,
ADD COLUMN IF NOT EXISTS bot_reason LowCardinality(String) DEFAULT '';
