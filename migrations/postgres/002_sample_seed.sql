-- Inserção de Cliente e Site padrão para testes iniciais
INSERT INTO clients (id, name)
VALUES ('11111111-1111-1111-1111-111111111111', 'HN Performance Digital')
ON CONFLICT (id) DO NOTHING;

INSERT INTO sites (id, client_id, domain, name, api_key, is_active)
VALUES (
    '22222222-2222-2222-2222-222222222222',
    '11111111-1111-1111-1111-111111111111',
    'hnperformancedigital.com.br',
    'HN Performance Principal',
    'hn_live_key_998877665544332211',
    true
)
ON CONFLICT (api_key) DO NOTHING;
