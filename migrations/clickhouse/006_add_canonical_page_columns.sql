-- Migration 006: Adicionar colunas de canonicalização de páginas
ALTER TABLE tracking_events.events 
ADD COLUMN IF NOT EXISTS page_key LowCardinality(String) DEFAULT '',
ADD COLUMN IF NOT EXISTS canonical_url String DEFAULT '';
