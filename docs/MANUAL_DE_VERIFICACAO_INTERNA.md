# Manual Técnico e Guia de Verificação Interna
**HN Performance Server-Side Tracking Engine**  
*Documento de Especificação Funcional, Auditoria de Conformidade (LGPD/ANPD), Telas e Configurações*  
*Versão da Plataforma: 2.5 (Produção) — Data: Setembro de 2026*

---

## 1. Visão Geral e Propósito da Plataforma

O **HN Tracking Engine** é uma infraestrutura distribuída de alta velocidade desenvolvida em **Go (Golang)** projetada para rastreamento de marketing de primeira parte (*1st-party*), atribuição multitouch de alta precisão e despacho de eventos via APIs de Conversão Server-Side (*CAPI*).

### Objetivos Principais:
1. **Neutralização de Restrições de Terceiros:** Contorna integralmente bloqueadores de anúncios (AdBlockers), Apple ITP (iOS 14.5+), Firefox ETP e o fim gradual dos cookies de terceiros no Google Chrome.
2. **Atribuição First-Touch & Multitouch Protegida:** Assegura a integridade das origens de tráfego (`utm_*`, `gclid`, `gbraid`, `wbraid`, `fbclid`, `ttclid`) desde o primeiro clique até a conversão final no WhatsApp, formulário ou CRM.
3. **Conformidade Estrita com LGPD / ANPD / GDPR:** Arquitetura orientada a *Privacy by Design & Default*, separando formalmente os papéis de Operador (plataforma) e Controlador (cliente), com controle granular de consentimento, mascaramento de IP/PII e expurgo seguro.
4. **Resiliência e Desempenho:** Ingestão HTTP sub-milissegundo em Go Fiber, buffer assíncrono em Redis Streams, persistência analítica colunar em ClickHouse e isolamento multi-tenant transacional em PostgreSQL.

---

## 2. Arquitetura de Componentes e Fluxo de Dados

```
                             VISITANTE / BROWSER
                                      │
              ┌───────────────────────┴───────────────────────┐
              ▼                                               ▼
       Página do Cliente                                Google Tag Manager
  (Tag SDK / sdk/tracker.js)                        (Container GTM Web / App)
              │                                               │
              └───────────────────────┬───────────────────────┘
                                      │ HTTP POST /api/v1/collect ou /t
                                      ▼
                        ┌───────────────────────────┐
                        │    Traefik Edge Proxy     │ (SSL Automático / Let's Encrypt)
                        └─────────────┬─────────────┘
                                      │
                                      ▼
                        ┌───────────────────────────┐
                        │  cmd/api (Fiber Service)  │
                        │ - Valida Site Key / Domínio│
                        │ - Respeita GPC/DNT e LGPD │
                        │ - Mascara IP e detecta Bot│
                        │ - Emite Cookie 1st-party  │
                        └─────────────┬─────────────┘
                                      │ XADD stream:events:raw
                                      ▼
                        ┌───────────────────────────┐
                        │    Redis 7 (Streams)      │ (Buffer Resiliente e Pub/Sub)
                        └──────┬─────────────┬──────┘
                               │             │
              ┌────────────────┘             └────────────────┐
              ▼                                               ▼
   ┌─────────────────────┐                         ┌─────────────────────┐
   │ cmd/ingester        │                         │ cmd/dispatcher      │
   │ (Consumo em Lote)   │                         │ (Despacho Concorrente│
   └──────────┬──────────┘                         └──────────┬──────────┘
              │                                               │
      ┌───────┴───────┐                         ┌─────────────┼─────────────┐
      ▼               ▼                         ▼             ▼             ▼
┌───────────┐   ┌──────────┐               ┌─────────┐   ┌─────────┐   ┌─────────┐
│PostgreSQL │   │ClickHouse│               │Meta CAPI│   │Google   │   │LinkedIn │
│(Identidade│   │(Eventos  │               │(Facebook│   │Ads / GA4│   │CAPI &   │
│& Metadata)│   │Analíticos│               │Direct)  │   │MP       │   │Webhooks │
└───────────┘   └──────────┘               └─────────┘   └─────────┘   └─────────┘
```

---

## 3. Catálogo Detalhado de Funcionalidades

### 3.1. Coletor HTTP de Ultra-Baixa Latência (`cmd/api`)
- **Endpoints de Ingestão:** `/api/v1/collect` (primário) e `/t` (compacto para payloads enxutos).
- **Resposta Instantânea:** Retorna `HTTP 202 Accepted` com `event_id` único gerado ou validado em tempo inferior a 2 milissegundos.
- **Tolerância a Carga:** Buffer em memória de conexões via fasthttp e enfileiramento não bloqueante em Redis Streams (`stream:events:raw`).
- **Prevenção de Sobrecarga:** Limitação de tamanho máximo de payload para 64 KB (`StatusRequestEntityTooLarge`).

### 3.2. Identidade Pseudônima & Cookie de 1ª Parte
- **Identificador de Visitante (`visitor_id`):** Emitido via cookie HTTP de 1ª parte (`_vid`), formatado como Type-Prefixed ID (`hn_vis_...`), política `SameSite=Lax`, flag `Secure` em produção e validade de 365 dias.
- **Autoridade Estrita no Servidor:** Tentativas do navegador ou de scripts de enviar identificadores arbitrários em `user_data.visitor_id` são descartadas, mitigando ataques de *Session Fixation* e contaminação de cookies (*Cookie Poisoning*).
- **Sessões Voláteis (`session_id`):** Geradas no cliente como `hn_ses_...` via `sessionStorage`, agrupando interações dentro de uma mesma aba de navegação.

### 3.3. Ingestão Analítica em Lote no ClickHouse (`cmd/ingester`)
- **Agrupamento Thread-Safe:** Consome eventos do stream usando Consumer Groups dedicados (`cg:ingester`).
- **Política de Flush Duplo:** Despeja em disco a cada **2.000 eventos** acumulados ou **2 segundos** decorridos, o que ocorrer primeiro.
- **Tabela `tracking_events.events`:**
  - Engine colunar `ReplacingMergeTree(created_at)`.
  - Particionamento mensal: `toYYYYMM(event_time)`.
  - Chave de ordenação analítica otimizada: `(site_id, event_name, event_time, visitor_id)`.
  - Colunas especializadas para ciclo de formulários: `submission_id`, `form_lifecycle_state`, `field_source`.
  - Colunas de telemetria defensiva: `is_bot`, `bot_reason`, `is_debug`, `origin_mode`.
- **Retenção Automatizada (TTL):** Política de expurgo mensal configurada no schema do ClickHouse para limpeza de eventos brutos após 90 a 730 dias.

### 3.4. Grafo de Identidade e Atribuição no PostgreSQL (`internal/identity`)
- **Proteção do Primeiro Toque (*First-Touch*):** O primeiro clique que trouxe o visitante ao site (URL, Landing Page, Referrer, UTMs e Click IDs) é gravado de forma imutável na tabela `visitors`.
- **Normalização e Pseudonimização:**
  - E-mails: remoção de espaços, lowercase, normalização de provedores (ex: descarte de aliases no Gmail).
  - Telefones: sanitização para formato internacional E.164 (apenas dígitos com código de país).
  - Criptografia HMAC-SHA256 utilizando `HMAC_PEPPER` secreto de aplicação para armazenamento seguro no banco de dados.

### 3.5. Dispatcher Server-Side Concorrente (`cmd/dispatcher`)
- **Mecanismo de Despacho:** Pool de workers assíncronos (configurável via `DISPATCHER_WORKERS`, padrão 8) com conexões HTTP persistentes (*keep-alive*).
- **Resiliência e Retries:** Retry exponencial com backoff em caso de instabilidade nas APIs de terceiros (até `DISPATCHER_MAX_RETRIES`, padrão 5).
- **Plataformas Homologadas:**
  1. **Meta Conversions API (CAPI):**
     - Despacho direto para o Graph API v19.0.
     - Hashes SHA-256 de e-mail e telefone (`em`, `ph`).
     - Preservação e formatação dos cookies `_fbp` e `_fbc` (baseado em `fbclid` e timestamp).
     - Deduplicação nativa via `event_id` com eventos disparados pelo Meta Pixel no frontend.
     - Suporte a `test_event_code` para depuração em tempo real no Gerenciador de Eventos da Meta.
  2. **Google Ads Offline / Enhanced Conversions:**
     - Envio de dados enriquecidos com hashes de contato, valor e moeda.
     - Suporte aos identificadores de clique: `gclid`, `gbraid` (iOS Web-to-App) e `wbraid` (iOS App-to-Web).
  3. **Google Analytics 4 Measurement Protocol:**
     - Envio de eventos estruturados para o endpoint oficial do GA4 (`/mp/collect`).
     - Rota de teste e validação de schema sem sujar relatórios via `/debug/mp/collect`.
  4. **LinkedIn Conversions API (Direct CAPI):**
     - Integração direta com a API Rest.li 2.0 do LinkedIn (`/rest/conversionEvents`).
     - **Versão Dinâmica de API:** Função `ResolveLinkedInAPIVersion` que calcula dinamicamente o ano/mês vigente em UTC (ex: `202609`) ou lê a variável `LINKEDIN_API_VERSION`, impedindo rejeições por versão expirada (`NONEXISTENT_VERSION` / HTTP 426).
     - **Diagnóstico Humanizado:** Mapeamento semântico de status HTTP com orientações claras para a equipe operacional (erros 401, 403, 404, 422, 426, 429).
  5. **Webhooks CRM / Automação (n8n, ActiveCampaign, HubSpot):**
     - Payload completo contendo dados do lead normalizados, jornada de Primeiro Contato (*First Touch*) e jornada de Conversão (*Conversion Touch*).
     - Assinatura de segurança via cabeçalho `X-Webhook-Secret` ou token Bearer.

### 3.6. SDK JavaScript de Borda (`sdk/tracker.js`)
- **Características do Script:** Vanilla JS minimalista (< 4 KB gzipped), zero dependências externas, compatível com todos os navegadores modernos e IE11.
- **Compatibilidade Nativa com GTM:** Resolução de `siteKey` e `endpoint` via atributos de script, `window.__HN_SITE_KEY__`, `data-gtmsrc` ou parâmetros de URL.
- **Rastreamento Automático:**
  - Ciclo de vida da sessão: `session_start`, `first_visit`, `page_view`.
  - Rolagem profunda de 90% (*Scroll Depth* padrão GA4).
  - Cliques em botões e links de WhatsApp (`wa.me`, `api.whatsapp.com`).
  - Downloads automáticos de arquivos (.pdf, .xlsx, .docx, .zip, etc.).
  - Cliques em links de saída para domínios externos (*Outbound Links*).
- **Captura Avançada de Formulários (Lifecycle de 3 Estágios):**
  1. `form_attempt`: disparo inicial da intenção de envio no DOM.
  2. `form_client_validated`: confirmação de que o formulário passou na validação do navegador (`form.checkValidity()`).
  3. `form_submit_success`: confirmação de sucesso emitida pelo backend do formulário ou evento dataLayer de construtores (Elementor, Bricks, WPForms, Fluent Forms, Contact Form 7).
- **Proteção contra Falsos Sucessos:** Intercepta e reconhece eventos de erro de construtores de formulários (`bricks/form/error`, `wpforms_error`, `form_submit_error`), marcando a submissão como inválida para impedir a criação de leads falsos.

---

## 4. Lógicas de Validação e Segurança Defensiva

| Camada | Validação Aplicada | Comportamento em Caso de Falha |
| :--- | :--- | :--- |
| **Tamanho do Payload** | Máximo 64 KB no corpo da requisição HTTP | HTTP 413 (*Request Entity Too Large*) |
| **Chave de API (`site_key`)** | Validação com cache em memória (`sync.Map`) e consulta transacional nas tabelas `site_api_keys` e `sites`. | HTTP 401 (*Unauthorized*), evento rejeitado. |
| **Whitelist de Domínios** | Extração do domínio via `Origin` ou `Referer` (com fallback seguro em server-side autenticado). Validação contra domínios autorizados do site, aceitando subdomínios automaticamente. | HTTP 403 (*Forbidden*), geração imediata de alerta de segurança em `site_security_alerts`. |
| **Prevenção de IP Spoofing** | Validação de `TRUSTED_PROXIES`. Cabeçalhos `CF-Connecting-IP`, `X-Real-IP` e `X-Forwarded-For` só são avaliados se a conexão TCP física provier de um CIDR autorizado. | Em caso de proxy não confiável, os cabeçalhos são sumariamente ignorados e adota-se o IP da conexão direta. |
| **Rate Limiting de Autenticação** | Limite de 5 tentativas de login por minuto por endereço IP (`/api/v1/auth/login`). | HTTP 429 (*Too Many Requests*) com mensagem explicativa e bloqueio de 60 segundos. |
| **Segregação Multi-Tenant** | Todas as queries no PostgreSQL e ClickHouse exigem a cláusula obrigatória `site_id = ?`. | Impossibilidade matemática de vazamento de dados entre diferentes sites ou contas. |
| **Validação de Credenciais CAPI** | Testes sintéticos com ping real e validação de formato antes de persistir no banco. | Diagnóstico detalhado de erro retornado ao operador sem comprometer a estabilidade do coletor. |

---

## 5. Dossiê de Privacidade e Conformidade LGPD / ANPD / GDPR

O HN Tracking Engine foi concebido sob a premissa de **Privacy by Design e Privacy by Default**:

### 5.1. Separação de Papéis: Operador vs. Controlador
- **Operador (A Plataforma):** Fornece os meios tecnológicos, o pipeline seguro, o armazenamento isolado e as ferramentas de auditoria e expurgo.
- **Controlador (O Cliente Proprietário do Site):** Decide as finalidades concretas, define a política de cookies informada aos titulares e obtém o consentimento quando exigível.

### 5.2. Tratamento Granular de Consentimento
A plataforma suporta três categorias essenciais de dados:
- **`necessary` (Essenciais):** Telemetria técnica agregada, prevenção a fraudes e segurança cibernética. Ativa por padrão (Art. 7º, IX e II da LGPD).
- **`analytics` (Analíticos):** Métricas de navegação e cookie pseudônimo de visitante (`_vid`). Exige consentimento ativo onde a política do site assim determinar.
- **`marketing` (Publicidade e Mídia Paga):** Captura e persistência de parâmetros de campanha (`utm_*`, `gclid`, `fbclid`) e despacho para redes de anúncios (Meta, Google, LinkedIn). **Desativado por padrão até manifestação afirmativa do titular.**

### 5.3. Respeito a Sinais Globais de Privacidade do Navegador
- **Global Privacy Control (`Sec-GPC: 1` ou `navigator.globalPrivacyControl`):** Se o navegador do titular emitir o sinal GPC, o SDK e o coletor revogam imediatamente a categoria de consentimento `marketing`, bloqueando a persistência de UTMs e impedindo o despacho de conversões para redes de anúncios externas.
- **Do Not Track (`DNT: 1`):** Respeitado no coletor conforme política configurada para o site.

### 5.4. Anonimização e Mascaramento de IP
- **IPv4:** O último octeto é zerado (ex: `187.33.241.45` converte-se em `187.33.241.0`, máscara `/24`).
- **IPv6:** Os últimos 80 bits são mascarados, preservando apenas o prefixo `/48`.
- Esse processo garante que o endereço IP registrado no banco não possa ser utilizado para individualizar o titular sem intervenção de provedores de conexão.

### 5.5. Hard Blocklist de Campos Sensíveis na Captura de Formulários
O SDK do tracker implementa uma lista de bloqueio estrita avaliada **antes** de qualquer tentativa de leitura de valores (`.value`):
- **Tags Bloqueadas:** `TEXTAREA`, `BUTTON`, `OBJECT`, `EMBED`.
- **Tipos Bloqueados:** `password`, `file`, `hidden`, `submit`, `reset`.
- **Padrões de Autocomplete Bloqueados:** `cc-*`, `credit-card`, `card-number`, `cvc`, `cvv`.
- **Padrões de Nome/ID Bloqueados:** Campos contendo `token`, `nonce`, `csrf`, `captcha`, `recaptcha`.
- **Resultado:** A plataforma **nunca** captura dados bancários, senhas ou mensagens privadas de visitantes.

### 5.6. Mascaramento de PII em Interfaces e Logs em Tempo Real
Para proteger a privacidade dos titulares perante operadores e analistas internos, qualquer visualização no DebugView ou em logs de streaming aplica mascaramento dinâmico:
- E-mails: `jo***@exemplo.com.br`
- Telefones: `21*****88`
- Nomes: `J*** S***`

### 5.7. Atendimento aos Direitos dos Titulares (Art. 18 LGPD)
Para atender a solicitações de exclusão ou acesso aos dados:
1. **Localização:** Busca por `site_id` + `visitor_id` (cookie `_vid`) ou pelo HMAC do e-mail/telefone informado pelo titular.
2. **Eliminação no PostgreSQL:** `DELETE FROM visitors WHERE site_id = $1 AND ...`
3. **Eliminação no ClickHouse:** `ALTER TABLE tracking_events.events DELETE WHERE site_id = $1 AND ...`
4. **Filas e Buffers:** Os registros em Redis expiram naturalmente em menos de 2 horas.

---

## 6. Mapeamento de Telas, Painéis e Campos do Dashboard

O dashboard web da plataforma (`/dashboard`) é estruturado em uma interface moderna com tema escuro (*dark mode*), visual *glassmorphism* e barra lateral retrátil (atalho de teclado: `Ctrl + B`).

### 6.1. Visão Geral (Aba `overview`)
- **Controles de Topo:**
  - Seletor de Período: Hoje (`today`), Ontem (`yesterday`), Últimos 7 dias (`7d` - padrão), Últimos 30 dias (`30d`), Este Mês (`this_month`).
  - Filtro por Domínio: permite isolar a análise para um domínio específico ou exibir o agregado da conta.
  - Indicador de Conexão com ClickHouse e Redis.
- **Cards de Métricas Principais (KPIs):**
  - *Total de Eventos:* soma acumulada de todos os disparos de telemetria recebidos.
  - *Visitantes Únicos:* contagem de identidades canônicas (`visitor_id`) distintas no período.
  - *Sessões Ativas:* volume de sessões únicas (`session_id`) iniciadas no período.
  - *Conversões Totais:* contagem de eventos qualificados (`lead`, `purchase`, `whatsapp_click`, `form_submit`, `contact`).
  - *Taxa de Conversão Geral:* percentual de visitantes que atingiram pelo menos uma conversão.
  - *Robôs Interceptados (Bot Shield):* total de requisições de robôs, crawlers e automações identificadas e isoladas, com contador de conversões protegidas contra custos falsos de mídia.
- **Gráficos e Visualizações:**
  - *Gráfico de Série Temporal:* evolução diária de visualizações de página vs. conversões.
  - *Distribuição de Dispositivos:* proporção de tráfego entre Desktop, Mobile e Tablet.
  - *Top Origens de Tráfego:* ranking de canais por `utm_source` e domínios de referência.

### 6.2. Páginas Monitoradas (Aba `pages`)
- **Tabela de URLs Analíticas:**
  - URL Canônica da Página (com link direto para abertura em nova aba).
  - Visualizações de Página (*PageViews*).
  - Visitantes Únicos na Página.
  - Tempo Médio de Permanência.
  - Conversões Geradas a partir daquela página.
  - Taxa de Conversão por Página.

### 6.3. Leads & Conversões (Aba `leads`)
- **Auditoria de Integridade de Tráfego Pago:**
  - Cada lead é auditado em tempo real quanto à completude de parâmetros de campanha:
    - 🟢 **OK:** Rastreamento completo (possui UTMs e Click ID da rede correspondente).
    - 🟡 **Aviso:** Parâmetros incompletos (ex: possui `utm_source`, mas falta `utm_campaign` ou Click ID).
    - 🔴 **Crítico:** Conversão sem nenhuma identificação de mídia paga ou origem.
- **Tabela de Detalhes da Conversão:**
  - Data e Hora exatas.
  - Nome do Evento (ex: `lead`, `purchase`, `whatsapp_click`).
  - Página de Conversão e Landing Page original de entrada.
  - Parâmetros: `utm_source`, `utm_medium`, `utm_campaign`, `gclid`, `fbclid`.
  - Dispositivo e IP mascarado.
- **Modal de Jornada Completa do Visitante:**
  - Ao clicar em um lead, abre a linha do tempo cronológica com todos os passos dados pelo visitante desde o primeiro acesso até a conversão.
- **Exportação em CSV:**
  - Botão de download de relatório com cabeçalho UTF-8 BOM e delimitador ponto-e-vírgula (`;`), garantindo abertura perfeita no Microsoft Excel e Google Sheets sem problemas de acentuação.

### 6.4. Origem & Campanhas (Aba `traffic`)
- Tabela detalhada de desempenho por Campanha (`utm_campaign`), Mídia (`utm_medium`) e Fonte (`utm_source`).
- Comparativo de volume de visitantes vs. leads qualificados gerados por cada canal de anúncio.

### 6.5. Funis & Atribuição (Aba `funnels`)
- **Funil de Conversão de 4 Estágios:**
  1. *Etapa 1 — Visualização de Página:* Total de acessos a páginas de conteúdo.
  2. *Etapa 2 — Engajamento & Rolagem:* Visitantes que rolaram pelo menos 90% da página ou permaneceram navegando.
  3. *Etapa 3 — Intenção / Início de Formulário:* Visitantes que clicaram no botão de WhatsApp ou iniciaram o preenchimento de um formulário (`form_attempt`).
  4. *Etapa 4 — Conversão Concluída:* Leads qualificados ou vendas confirmadas (`form_submit_success`, `purchase`).
  - *Métrica de Abandono (Drop-off):* Exibição visual da perda percentual de usuários entre cada etapa.
- **Caminhos de Atribuição Multitouch:**
  - Ranking das top sequências de canais percorridas pelos visitantes até converterem (ex: `Google Ads > Orgânico > WhatsApp`).
  - Comparativo entre o modelo de *Primeiro Toque (First Touch)* e *Último Toque (Last Touch)*.

### 6.6. DebugView em Tempo Real (Aba `debug`)
- **Transmissão ao Vivo via Server-Sent Events (SSE):**
  - Conexão em tempo real através do endpoint `/api/v1/debug/stream`.
  - Histórico instantâneo dos últimos 50 eventos gravados no buffer volátil do Redis.
- **Filtros de Depuração:**
  - Filtro por tipo de evento (Todos, Apenas Conversões, PageViews, Formulários).
  - Filtro por Origem: Eventos de Produção (`origin_mode: production`) vs. Eventos de Teste (`origin_mode: debug`).
- **Simulador de Eventos Sintéticos:**
  - Formulário integrado para disparar eventos de teste diretamente da interface com parâmetros customizados para validar o pipeline sem sujar os relatórios analíticos.
- **Botão de Limpeza:** Exclui o buffer temporário de depuração com 1 clique.

### 6.7. Assistente de Envios Server-Side / CAPI (Wizard)
- Modal acessível pelo menu lateral com configuração passo a passo e botão de teste (*Ping*) para cada plataforma:
  - **Meta Conversions API (CAPI):**
    - Campos: *Pixel ID*, *Access Token*, *Test Event Code* (opcional).
    - Botão de Teste: executa envio sintético e exibe a resposta exata do Graph API da Meta.
  - **Google Ads Offline / Enhanced Conversions:**
    - Campos: *Endpoint URL / Webhook*, *API Key / Bearer Token*, *Conversion Action*.
    - Botão de Teste: valida conectividade e autenticação.
  - **Google Analytics 4 Measurement Protocol:**
    - Campos: *Measurement ID* (ex: `G-XXXXXXXXXX`), *API Secret*.
    - Botão de Teste: valida contra o endpoint de depuração `/debug/mp/collect` do Google.
  - **LinkedIn Conversions API:**
    - Campos: *Ad Account ID*, *Conversion Rule ID*, *OAuth2 Access Token*.
    - Botão de Teste: validação com diagnóstico inteligente humanizado (status 200, 401, 403, 404, 422, 426).
  - **Webhook de CRM / n8n:**
    - Campos: *Webhook URL*, *Secret Token*.
    - Botão de Teste: dispara payload de demonstração com estrutura de contato e jornada.

### 6.8. Configurações do Site (Aba `settings`)
- **Gestão de Chaves de API (`site_api_keys`):**
  - Visualização de chaves ativas com máscara de segurança.
  - Botão de criação de nova chave com nome identificador.
  - Telemetria de uso: total de eventos processados pela chave, data/hora do último uso e status operacional (*Em uso*, *Aguardando tráfego*, *Inativa*, *Revogada*).
  - Botão de Revogação Imediata: invalida a chave no banco e expurga instantaneamente do cache em memória (`sync.Map`), bloqueando qualquer requisição subsequente.
- **Configuração de Integrações:** Permite atualizar chaves e credenciais ativas diretamente no banco relacional.

### 6.9. Gestão de Domínios e Alertas de Segurança (Aba `domains`)
- **Lista de Domínios Permitidos (Whitelist):**
  - Tabela com todos os domínios homologados para ingestão de dados.
  - Botão de adição de novo domínio.
  - Botão de remoção de domínio existente.
- **Central de Alertas de Domínios Não Autorizados:**
  - Tabela de incidentes contendo: Data/Hora, Domínio Invasor/Desconhecido, IP de Origem, User-Agent e URL Completa.
  - **Botão de Aprovação com 1 Clique:** Permite ao administrador homologar imediatamente o domínio caso trate-se de uma landing page ou subdomínio legítimo recém-publicado.

---

## 7. Matriz de Variáveis de Ambiente e Configuração

| Variável | Padrão | Obrigatória em Produção? | Descrição e Impacto |
| :--- | :--- | :--- | :--- |
| `ENV` | `development` | Sim | Define o modo de operação (`development` ou `production`). Em produção, ativa cookies `Secure`, validação estrita de senhas e suprime logs verbosos. |
| `TRACKING_DOMAIN` | `localhost` | Sim | Hostname principal do coletor (ex: `trackeamento.hnperformancedigital.com.br`). Utilizado para validação de CORS administrativo. |
| `HTTP_PORT` | `8080` | Não | Porta TCP interna em que o servidor Fiber escuta. |
| `ADMIN_USER` | `admin` | Sim | Usuário mestre para login no painel administrativo `/dashboard`. |
| `ADMIN_PASSWORD` | - | Sim (Mín. 16 chars) | Senha do administrador mestre. O sistema recusa inicializar em produção se contiver placeholders ou for fraca. |
| `SERVER_API_KEY` | - | Sim (Mín. 16 chars) | Chave secreta interna para requisições server-side autenticadas via cabeçalho `X-Server-Key`. |
| `HMAC_PEPPER` | - | Sim (Mín. 16 chars) | Sal secreto de aplicação para hashing de dados pessoais (e-mails e telefones) perante a LGPD. |
| `TRUSTED_PROXIES` | *Vazio* | Sim | Lista de blocos CIDR de proxies reversos confiáveis (ex: `127.0.0.1,172.16.0.0/12,10.0.0.0/8`). Bloqueia categoricamente ataques de IP Spoofing. |
| `REDIS_ADDR` | `127.0.0.1:6379` | Sim | Endereço do cluster/instância Redis para buffer de streams e Pub/Sub. |
| `REDIS_PASSWORD` | - | Sim | Senha de autenticação do Redis. |
| `REDIS_STREAM_RAW` | `stream:events:raw`| Não | Nome da fila/stream principal de eventos brutos recebidos pelo coletor. |
| `REDIS_CONSUMER_GROUP` | `tracking_workers`| Não | Nome do grupo consumidor compartilhado entre os workers. |
| `POSTGRES_HOST` | `127.0.0.1` | Sim | Host do banco relacional PostgreSQL 16. |
| `POSTGRES_PORT` | `5432` | Não | Porta do PostgreSQL. |
| `POSTGRES_DB` | `tracking_db` | Sim | Nome da base de dados relacional. |
| `POSTGRES_USER` | `tracking_user` | Sim | Usuário do PostgreSQL. |
| `POSTGRES_PASSWORD` | - | Sim | Senha do PostgreSQL. |
| `CLICKHOUSE_ADDR` | `127.0.0.1:9000` | Sim | Endereço da interface nativa TCP do ClickHouse Server. |
| `CLICKHOUSE_DB` | `tracking_events`| Sim | Nome do banco colunar analítico. |
| `CLICKHOUSE_USER` | `default` | Sim | Usuário de conexão do ClickHouse. |
| `CLICKHOUSE_PASSWORD` | - | Sim | Senha do ClickHouse. |
| `INGESTER_BATCH_SIZE` | `2000` | Não | Limite máximo de eventos em memória antes do flush forçado no ClickHouse. |
| `INGESTER_FLUSH_INTERVAL_SEC` | `2` | Não | Intervalo máximo em segundos para gravação de lotes analíticos. |
| `DISPATCHER_WORKERS` | `8` | Não | Quantidade de rotinas concorrentes para envio server-side CAPI. |
| `DISPATCHER_MAX_RETRIES`| `5` | Não | Tentativas máximas de reenvio em caso de falha de rede externa. |
| `DISPATCHER_TIMEOUT_SEC`| `10` | Não | Timeout máximo em segundos para cada chamada HTTP externa. |
| `SEED_DEFAULT_SITE` | `false` | Não | Se `true`, realiza bootstrap automático de um site padrão caso o banco esteja vazio. |
| `LINKEDIN_API_VERSION` | *Dinâmico* | Não | Versão da API do LinkedIn no formato `YYYYMM`. Se vazia, a aplicação calcula dinamicamente o mês atual em UTC. |

---

## 8. Protocolo e Checklist de Verificação Interna (QA / Auditoria)

Este protocolo deve ser executado antes de cada release em produção ou auditoria de conformidade:

### Checklist 1: Ingestão e Segurança de Borda
- [ ] O endpoint `/api/v1/collect` responde `HTTP 202 Accepted` em < 10ms para requisições válidas.
- [ ] Requisições sem `site_key` ou com chave inválida recebem `HTTP 401 Unauthorized`.
- [ ] Requisições com payload acima de 64 KB recebem `HTTP 413 Request Entity Too Large`.
- [ ] O cookie `_vid` emitido possui `Path=/`, `SameSite=Lax`, expiração de 1 ano e formato `hn_vis_...`.
- [ ] Se o cabeçalho `Sec-GPC: 1` for enviado, o evento é marcado sem consentimento de marketing e sem disparo de anúncios externos.
- [ ] Requisições originadas de domínios fora da whitelist recebem `HTTP 403 Forbidden` e geram alerta imediato na aba de Domínios.

### Checklist 2: Resiliência de Streaming e Ingestão
- [ ] Os eventos recebidos na API constam imediatamente no Redis Stream `stream:events:raw`.
- [ ] O serviço `cmd/ingester` acumula e persiste os eventos na tabela `tracking_events.events` sem perda de pacotes.
- [ ] Eventos marcados como `is_bot = 1` possuem a coluna `bot_reason` preenchida e são computados no card de segurança sem poluir as métricas limpas de conversão.

### Checklist 3: Despacho Server-Side (CAPI)
- [ ] Disparo de teste para Meta CAPI retorna sucesso ou mensagem descritiva da Graph API.
- [ ] Disparo de teste para Google Ads / GA4 MP valida a formatação sem rejeição de schema.
- [ ] Disparo de teste para LinkedIn CAPI retorna diagnóstico semântico compreensível na UI.
- [ ] Webhook de CRM recebe dados normalizados e agrupados por *First Touch* e *Conversion Touch*.

### Checklist 4: Painel Administrativo e Relatórios
- [ ] O seletor de períodos atualiza os cards de métricas e gráficos sem erros no console.
- [ ] A exportação de leads em CSV gera arquivo com acentuação correta e separador ponto-e-vírgula (`;`).
- [ ] O DebugView transmite eventos em tempo real via SSE e mascara e-mails, telefones e nomes de visitantes.
- [ ] O atalho de teclado `Ctrl + B` expande e recolhe a barra lateral do dashboard.
