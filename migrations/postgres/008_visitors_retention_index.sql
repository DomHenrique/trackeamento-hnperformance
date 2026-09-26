-- Migração 008: Adiciona índice para otimização de rotinas de expurgo e purga periódica por retenção
CREATE INDEX IF NOT EXISTS idx_visitors_retention ON visitors (site_id, last_seen_at);
