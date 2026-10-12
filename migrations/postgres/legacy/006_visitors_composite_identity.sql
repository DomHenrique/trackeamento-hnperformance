-- Migration 006: Identidade Composta de Visitantes e Chave Externa Desassociada

-- 1. Garante que a chave primária id tenha geração automática gen_random_uuid()
ALTER TABLE visitors ALTER COLUMN id SET DEFAULT gen_random_uuid();

-- 2. Adiciona coluna visitor_id VARCHAR(64) se não existir
ALTER TABLE visitors ADD COLUMN IF NOT EXISTS visitor_id VARCHAR(64);

-- 3. Migração retroativa: preenche visitor_id a partir de id::text para registros legados
UPDATE visitors SET visitor_id = id::text WHERE visitor_id IS NULL;

-- 4. Torna visitor_id estritamente NOT NULL
ALTER TABLE visitors ALTER COLUMN visitor_id SET NOT NULL;

-- 5. Adiciona restrição de unicidade composta (site_id, visitor_id) para isolamento estrito multi-site
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'uq_visitors_site_visitor'
    ) THEN
        ALTER TABLE visitors ADD CONSTRAINT uq_visitors_site_visitor UNIQUE (site_id, visitor_id);
    END IF;
END $$;

-- 6. Índice de alta performance para busca e upsert por par (site_id, visitor_id)
CREATE INDEX IF NOT EXISTS idx_visitors_site_visitor_id ON visitors(site_id, visitor_id);
