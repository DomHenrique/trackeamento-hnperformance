-- Migration 011: Blindagem de Integridade e Eliminação de Shadow Keys em site_api_keys

-- 1. Backfill de registros legados que eventualmente possuam name ou created_by nulos
UPDATE site_api_keys
SET name = 'Chave de Acesso'
WHERE name IS NULL OR TRIM(name) = '';

UPDATE site_api_keys
SET created_by = 'sistema'
WHERE created_by IS NULL OR TRIM(created_by) = '';

-- 2. Aplicação de restrições NOT NULL e DEFAULT para garantir persistência íntegra
ALTER TABLE site_api_keys
    ALTER COLUMN name SET NOT NULL,
    ALTER COLUMN name SET DEFAULT 'Chave de Acesso',
    ALTER COLUMN created_by SET NOT NULL,
    ALTER COLUMN created_by SET DEFAULT 'sistema';
