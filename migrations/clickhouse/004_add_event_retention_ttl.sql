-- 004_add_event_retention_ttl.sql: Define ciclo de vida e expurgo automatizado de eventos analíticos (LGPD / Privacy by Design)
-- Expira eventos brutos após 90 dias com base no timestamp UTC do evento (event_time).

ALTER TABLE tracking_events.events MODIFY TTL event_time + INTERVAL 90 DAY;

-- Se a tabela de migração events_v2 existir, aplica a mesma cláusula de expurgo
ALTER TABLE tracking_events.events_v2 MODIFY TTL event_time + INTERVAL 90 DAY;
