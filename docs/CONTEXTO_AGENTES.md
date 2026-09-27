# Contexto de Arquitetura & Guia Operacional para Agentes de IA
**HN Performance Server-Side Tracking Engine**  
*Documento de Referência Rápida e Decisões de Design para Agentes Autônomos e Engenheiros*  
*Versão: 2.5 — Data: Setembro de 2026*

---

## 1. Visão Geral em Uma Frase
O **HN Tracking Engine** é uma plataforma server-side de alto desempenho escrita em **Go** para ingestão analítica colunar em **ClickHouse**, buffer em **Redis Streams**, persistência relacional/identidade em **PostgreSQL** e despacho server-side para **Meta CAPI, Google Ads, GA4, LinkedIn CAPI e Webhooks de CRM**, com conformidade rigorosa com a **LGPD (Privacy by Design)**.

---

## 2. Mapa Estrutural do Repositório

```
Trackeamento HN/
├── cmd/
│   ├── api/                     # Ponto de entrada da API HTTP Fiber (Coletor, Auth, Dashboard, Docs, Debug)
│   │   ├── main.go              # Rotas HTTP, middlewares defensivos (Helmet, Limiter, CORS segregado)
│   │   ├── dashboard.html       # SPA completa do Dashboard Analítico (Glassmorphism, 9 seções)
│   │   ├── docs.html            # Página pública de documentação do SDK para clientes
│   │   └── landing.html         # Landing page de status e apresentação institucional
│   ├── ingester/                # Worker de ingestão em lote no ClickHouse
│   │   └── main.go              # Consumo contínuo de Redis Streams e flush colunar
│   └── dispatcher/              # Worker de despacho de conversões server-side (CAPI)
│       └── main.go              # Pool concorrente com retries exponenciais e envio multi-plataforma
├── internal/
│   ├── attribution/             # Parser de URLs, sanitização e extração de UTMs e Click IDs
│   ├── auth/                    # Autenticação de operadores, hash bcrypt, sessões e rate limit
│   ├── collector/               # Handler da API de coleta, detecção de robôs, alertas e governança
│   │   ├── analytics.go         # Consultas de overview, funil, caminhos de atribuição e exportação CSV
│   │   ├── bot_detector.go      # Heurísticas de detecção passiva de bots e crawlers
│   │   ├── bot_stats.go         # Agregação analítica de tráfego de robôs interceptados
│   │   ├── domain.go            # Whitelist de domínios, alertas de invasão e aprovação 1-clique
│   │   ├── event.go             # Estruturas centrais EventRequest e EventPayload
│   │   ├── handler.go           # Pipeline do coletor: valida chave, cookie _vid, IP e enfileira
│   │   └── privacy.go           # Modelagem de consentimento (LGPD/GPC) e mascaramento de IP
│   ├── config/                  # Carregamento e validação defensiva de variáveis de ambiente
│   ├── dispatcher/              # Lógica de distribuição assíncrona para plataformas de mídia
│   ├── identity/                # Grafo de identidade (Postgres), normalização de e-mail/telefone e HMAC
│   ├── ingester/                # Acumulador em memória thread-safe e batch flush no ClickHouse
│   ├── integhandler/            # Handlers REST para gestão de chaves de API e integrações CAPI
│   ├── integrations/            # Clientes HTTP especializados (Meta CAPI, Google Ads, GA4, LinkedIn, CRM)
│   ├── prefixedid/              # Gerador criptográfico de Type-Prefixed IDs (hn_site_, hn_vis_, etc.)
│   └── storage/                 # Conectores de banco (ClickHouse, Postgres, Redis) e queries analíticas
├── migrations/
│   ├── clickhouse/              # Schemas DDL e mutações da tabela tracking_events.events
│   └── postgres/                # Schemas DDL relacionais (sites, chaves, domínios, visitantes, alertas)
├── sdk/
│   └── tracker.js               # SDK Vanilla JS (< 4KB) para navegadores e GTM com form lifecycle
├── docs/                        # Documentação técnica, manuais de verificação e governança
│   ├── MANUAL_DE_VERIFICACAO_INTERNA.md  # Dossiê completo de testes e funcionalidades
│   ├── PRIVACY_AND_DATA_GOVERNANCE.md    # Especificação formal LGPD/ANPD/GDPR
│   └── CONTEXTO_AGENTES.md               # Este arquivo
├── docker-compose.yml           # Orquestração local/staging
├── docker-compose.prod.yml      # Orquestração de produção com Traefik
├── Makefile                     # Atalhos de compilação, testes e execução
└── README.md                    # Documentação principal do repositório
```

---

## 3. Padrão de Identificadores (Type-Prefixed IDs)

O projeto adota o padrão moderno de identificadores tipados com prefixo (inspirado na Stripe):

| Prefixo | Entidade Representada | Exemplos de Formato | Onde é Gerado |
| :--- | :--- | :--- | :--- |
| `hn_site_` | Identificador do Site / Tenant | `hn_site_8899aabbccddeeff` | PostgreSQL (`sites.id`) |
| `hn_live_key_` | Chave de API Pública de Produção | `hn_live_key_0123456789abcdef` | PostgreSQL (`site_api_keys.key`) |
| `hn_test_key_` | Chave de API Pública de Teste | `hn_test_key_fedcba9876543210` | PostgreSQL (`site_api_keys.key`) |
| `hn_vis_` | Identificador de Visitante Canônico | `hn_vis_1234567890abcdef12345678` | Coletor Go (`_vid` cookie) |
| `hn_ses_` | Identificador de Sessão Ativa | `hn_ses_abcdef0123456789abcdef01` | SDK JS (`sessionStorage`) |
| `hn_evt_` | Identificador de Evento (Deduplicação) | `hn_evt_9876543210fedcba98765432` | SDK JS / Coletor Go |
| `hn_dom_` | Identificador de Domínio Autorizado | `hn_dom_456789abcdef0123456789ab` | PostgreSQL (`site_allowed_domains`) |

---

## 4. Ciclo de Vida do Evento Ponta a Ponta

```
1. SDK (tracker.js)
   └─ Dispara trackEvent(eventName, userData, customData)
   └─ Avalia hard blocklist de senhas/cartões
   └─ Monta client_signals (webdriver, headless, screen, time_on_page)
   └─ Envia via navigator.sendBeacon ou fetch keepalive

2. Coletor HTTP (cmd/api)
   └─ Valida tamanho do payload (máx. 64 KB)
   └─ Valida site_key com cache sync.Map
   └─ Valida Domínio de Origem contra a Whitelist (Origin / Referer)
   └─ Valida TRUSTED_PROXIES para extração segura de IP real (anti-spoofing)
   └─ Aplica mascaramento de IP (conforme LGPD: último octeto zerado)
   └─ Respeita sinais GPC / DNT (desativa marketing se ativo)
   └─ Emite ou renova o cookie 1st-party _vid (1 ano, Lax, Secure)
   └─ Detecta robô / automação (DetectBot)
   └─ Responde HTTP 202 Accepted imediatamente (< 2ms)
   └─ Transmite para o DebugView via Redis Pub/Sub (com PII mascarada)
   └─ Publica o evento bruto no Redis Stream: stream:events:raw

3. Ingestão Analítica (cmd/ingester)
   └─ Consome de stream:events:raw (Consumer Group cg:ingester)
   └─ Agrupa em lotes na memória (até 2.000 eventos ou a cada 2s)
   └─ Grava em lote na tabela tracking_events.events do ClickHouse
   └─ Atualiza o perfil e First-Touch do visitante no PostgreSQL (sob HMAC-SHA256)

4. Despacho Server-Side (cmd/dispatcher)
   └─ Consome de stream:events:raw (Consumer Group cg:dispatcher)
   └─ Verifica se o evento é qualificado para envio e se consent.marketing == true
   └─ Consulta integrações ativas do site no Postgres (Meta, Google, LinkedIn, CRM)
   └─ Despacha em paralelo com retries e backoff exponencial
```

---

## 5. Regras Críticas de Não-Regressão

Ao realizar qualquer alteração no código, **NUNCA** viole as seguintes diretrizes:

1. **Autoridade Estrita do Servidor no `visitor_id`:**
   - O identificador de visitante é emitido com autoridade única pelo coletor Go no cookie `_vid`.
   - Se o cliente enviar `userData.visitor_id`, o coletor deve **descartar** esse valor para evitar *Session Fixation* e *Cookie Poisoning*.
2. **Hard Blocklist de Campos no SDK:**
   - Nunca remova a função `isFieldBlocked` do `tracker.js`. Campos de senha (`password`), cartão de crédito (`cc-*`), arquivos (`file`), campos ocultos (`hidden`) e tokens CSRF jamais podem ter seu `.value` lido.
3. **Respeito ao Global Privacy Control (GPC):**
   - Se o cabeçalho `Sec-GPC: 1` ou o sinal JS `navigator.globalPrivacyControl` estiver ativo, a categoria de consentimento `marketing` deve ser revogada sumariamente, impedindo a persistência de UTMs no `localStorage` e o despacho para plataformas de mídia paga.
4. **Isolamento Multi-Tenant Estrito:**
   - Toda consulta no ClickHouse e PostgreSQL deve conter a cláusula `WHERE site_id = ?`. Nunca faça queries globais que misturem dados de clientes.
5. **Prevenção de IP Spoofing:**
   - Nunca confie cegamente em `X-Forwarded-For` ou `CF-Connecting-IP`. Sempre utilize o helper `ResolveClientIP`, que valida se o IP da conexão física reside em `TRUSTED_PROXIES`.
6. **Mascaramento de Dados Pessoais em Telas e Logs:**
   - Logs de streaming do DebugView e endpoints públicos nunca devem exibir e-mails, nomes ou telefones em claro. Utilize sempre `maskUserData`.

---

## 6. Comandos e Operações do Desenvolvedor

### Compilação e Testes Locais
```bash
# Executar todos os testes automatizados da aplicação
go test -v -race ./...

# Testar especificamente o módulo de coleta e segurança
go test -v ./internal/collector/...

# Testar integridade das integrações (LinkedIn CAPI, Meta, GA4)
go test -v ./internal/integrations/...

# Testar normalização de identidade e HMAC
go test -v ./internal/identity/...

# Compilar todos os binários
make build
# Ou individualmente:
go build -o bin/api ./cmd/api
go build -o bin/ingester ./cmd/ingester
go build -o bin/dispatcher ./cmd/dispatcher
```

### Inicialização com Docker
```bash
# Subir ambiente local completo (Postgres, ClickHouse, Redis, API, Ingester, Dispatcher)
docker compose up -d --build

# Verificar logs dos serviços
docker compose logs -f api
docker compose logs -f ingester
docker compose logs -f dispatcher
```
