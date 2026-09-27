-- Migration 005: Adicionar colunas para ciclo de vida de formulários e mapeamento de campos (Entrega A)
ALTER TABLE tracking_events.events 
ADD COLUMN IF NOT EXISTS submission_id String DEFAULT '',
ADD COLUMN IF NOT EXISTS form_lifecycle_state LowCardinality(String) DEFAULT '',
ADD COLUMN IF NOT EXISTS field_source String DEFAULT '';
