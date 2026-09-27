# 🚀 HN Server-Side Tracking Engine

Plataforma corporativa de alta escala para **rastreamento server-side de 1ª parte (*1st-party*)**, **atribuição multitouch**, **despacho assíncrono de conversões (CAPI)** e **governança estrita de dados (LGPD / ANPD / GDPR)**.

Desenvolvida em **Go (Golang)**, com processamento analítico colunar em **ClickHouse**, banco relacional transacional em **PostgreSQL**, filas e buffers em tempo real via **Redis Streams** e terminação TLS com certificados automáticos via **Traefik**.

Projetada para neutralizar perdas de dados causadas por bloqueadores de anúncios (AdBlockers), Apple ITP (iOS 14.5+), restrições de navegadores e assegurar **100% de integridade na atribuição do primeiro clique** até a conversão final no WhatsApp, formulário ou CRM.

---

## 🏛️ Arquitetura do Sistema

```
                    INTERNET / SITES CLIENTES / GTM
                               │
                               ▼
                        ┌─────────────┐
                        │   Traefik   │ (SSL Let's Encrypt / Proxy Reverso Borda)
                        └──────┬──────┘
                               │
                               ▼
                        ┌─────────────┐
                        │   Go API    │ (Coletor HTTP sub-milissegundo)
                        │  Tracking   │ (Set-Cookie _vid 1st-party + Anti-Bot)
                        └──────┬──────┘
                               │ XADD stream:events:raw
                               ▼
                        ┌─────────────┐
                        │    Redis    │ (Stream / Buffer de Ingestão Resiliente)
                        └──────┬──────┘
                               │
                  ┌────────────┴────────────┐
                  ▼                         ▼
       ┌─────────────────────┐   ┌─────────────────────┐
       │  Go Batch Ingester  │   │    Go Dispatcher    │
       │ (Flush a cada 2s /  │   │ (Pool de Workers    │
       │  2.000 eventos)     │   │  com Retry & CAPI)  │
       └──────────┬──────────┘   └──────────┬──────────┘
                  │                         │
         ┌────────┴────────┐      ┌─────────┼─────────┬─────────┐
         ▼                 ▼      ▼         ▼         ▼         ▼
   ┌───────────┐    ┌──────────┐┌────┐  ┌──────┐  ┌────────┐  ┌─────┐
   │PostgreSQL │    │ClickHouse││Meta│  │Google│  │LinkedIn│  │ CRM │
   │Identidade │    │ Eventos  ││CAPI│  │ Ads  │  │  CAPI  │  │ n8n │
   │& Metadados│    │ (OLAP)   │└────┘  └──────┘  └────────┘  └─────┘
   └───────────┘    └──────────┘
```

---

## ✨ Principais Funcionalidades da Plataforma

### 1. Coletor HTTP de Ultra-Baixa Latência (`cmd/api`)
- **Resposta Instantânea:** Responde em tempo sub-milissegundo com status `HTTP 202 Accepted` e identificador único de evento (`hn_evt_...`).
- **Cookie de 1ª Parte Canônico (`_vid`):** Gerado com autoridade exclusiva no servidor (`hn_vis_...`), política `SameSite=Lax`, flag `Secure` em produção e validade de 365 dias. Tentativas de fixação arbitrária no cliente são descartadas.
- **Proteção Contra IP Spoofing:** Validação estrita via lista `TRUSTED_PROXIES`. Cabeçalhos `CF-Connecting-IP`, `X-Real-IP` e `X-Forwarded-For` só são processados quando a conexão TCP física provém de um CIDR autorizado.
- **Segurança de Borda & Anti-Bot:** Detecção e isolamento de tráfego de robôs, crawlers e automações (`is_bot = 1`) para proteger métricas limpas e evitar desperdício de investimento em anúncios.
- **Whitelist de Domínios em Tempo Real:** Valida a origem da requisição e gera alertas imediatos em caso de tentativas de envio por domínios não cadastrados.

### 2. Ingestão Analítica em Lote no ClickHouse (`cmd/ingester`)
- **Processamento em Lote Thread-Safe:** Consome de Redis Streams e executa *flush* no banco analítico a cada **2.000 eventos** ou **2 segundos**.
- **Tabela `tracking_events.events`:** Engine colunar `ReplacingMergeTree(created_at)` com particionamento mensal e ordenação analítica por `(site_id, event_name, event_time, visitor_id)`.
- **Campos Especializados de Ciclo de Vida:** Suporte a `submission_id`, `form_lifecycle_state` e `field_source`.
- **Retenção Automatizada (TTL):** Política configurável de expurgo automático de eventos antigos.

### 3. Grafo de Identidade e Atribuição First-Touch (`internal/identity`)
- **Proteção do Primeiro Toque:** O primeiro contato do visitante com o site (URL, Landing Page, Referrer, UTMs e Click IDs) é gravado de forma imutável no PostgreSQL.
- **Normalização e Pseudonimização:** Normalização rigorosa de e-mails e telefones, armazenados sob hash criptográfico HMAC-SHA256 utilizando `HMAC_PEPPER` de aplicação perante a LGPD.

### 4. Dispatcher Server-Side Concorrente (`cmd/dispatcher`)
- **Pool de Conexões HTTP Keep-Alive:** Despacho paralelo com retry exponencial para máxima taxa de entrega.
- **Integrações Nativas Server-Side:**
  - **Meta Conversions API (CAPI):** Despacho direto com hashes SHA-256 (`em`, `ph`), deduplicação de `event_id` com o Meta Pixel, cookies `_fbp`/`_fbc` e suporte a código de teste (`test_event_code`).
  - **Google Ads Offline / Enhanced Conversions:** Envio de dados enriquecidos com hashes de contato e suporte a `gclid`, `gbraid` e `wbraid`.
  - **Google Analytics 4 Measurement Protocol:** Suporte completo com rota de validação de schema `/debug/mp/collect`.
  - **LinkedIn Conversions API (Direct CAPI):** Suporte nativo à API Rest.li 2.0 do LinkedIn com **cálculo dinâmico da versão ativa da API** (prevenindo erros de versão expirada) e diagnóstico semântico humanizado de falhas na interface.
  - **Webhooks de CRM / n8n:** Disparo de payloads enriquecidos contendo contato do lead, dados do *Primeiro Toque* (*First Touch*) e da *Conversão* (*Conversion Touch*).

### 5. SDK JavaScript de Borda (`sdk/tracker.js`)
- **Ultra-Leve e Zero Dependências:** Vanilla JS puro (< 4 KB gzipped), sem impacto nos Core Web Vitals (LCP/FID).
- **Suporte Nativo ao GTM:** Compatível com Google Tag Manager, Tag Assistant e variáveis globais.
- **Eventos Automáticos:** `session_start`, `first_visit`, `page_view`, rolagem de 90% (*Scroll Depth*), cliques em WhatsApp, downloads de arquivos e cliques em links de saída (*Outbound Links*).
- **Captura Avançada de Formulários (Lifecycle de 3 Estágios):**
  1. `form_attempt`: tentativa de envio disparada no DOM.
  2. `form_client_validated`: validação nativa de preenchimento do navegador sem reenvio de dados sensíveis.
  3. `form_submit_success`: confirmação de sucesso disparada pelo servidor ou eventos dataLayer de construtores (Bricks, Elementor, WPForms, Fluent Forms, Contact Form 7).
- **Prevenção de Falsas Conversões:** Interceptação automática de erros de builders de formulário para impedir disparos de leads incorretos.

---

## 🔒 Governança de Dados e Conformidade LGPD / ANPD

A plataforma implementa a metodologia **Privacy by Design e Privacy by Default**:

1. **Separação Formal de Papéis:** A infraestrutura atua como **Operador** técnico; o cliente proprietário do site atua como **Controlador** das bases legais e consentimento.
2. **Consentimento Granular:** Suporte às categorias `necessary`, `analytics` e `marketing`. A persistência de parâmetros de anúncio e o despacho externo para Meta/Google/LinkedIn permanecem bloqueados até o consentimento ativo do titular.
3. **Respeito ao Global Privacy Control (GPC):** Reconhece cabeçalhos `Sec-GPC: 1` e a flag JS `navigator.globalPrivacyControl`, revogando automaticamente categorias de marketing.
4. **Anonimização de IP:** Mascaramento do último octeto em conexões IPv4 (`187.33.241.0/24`) e mascaramento dos últimos 80 bits em IPv6 (`/48`).
5. **Hard Blocklist de Campos Sensíveis:** O SDK recusa terminantemente a leitura de campos de senha (`password`), cartões de crédito (`cc-*`), tokens CSRF, campos ocultos (`hidden`) e anexos de arquivos.
6. **Mascaramento em Logs e Telas:** O DebugView em tempo real e os logs operacionais mascaram todos os dados de contato (`jo***@exemplo.com.br`, `21*****88`, `J*** S***`).
7. **Atendimento a Direitos do Titular (Art. 18 LGPD):** Rotinas completas de localização e eliminação de registros por `visitor_id` ou HMAC de e-mail/telefone no PostgreSQL e ClickHouse.

---

## 📊 Dashboard Analítico Completo (`/dashboard`)

O painel administrativo possui interface moderna em Dark Mode / Glassmorphism, com sidebar retrátil (atalho: `Ctrl + B`) e 9 módulos de inteligência:

1. **📊 Visão Geral (Overview):** KPIs em tempo real (Eventos, Visitantes Únicos, Sessões, Conversões, Taxa de Conversão e Tráfego de Robôs Interceptados), gráfico de série temporal, distribuição de dispositivos e top origens.
2. **📄 Páginas Monitoradas:** Tabela de URLs canônicas com visualizações, visitantes únicos, tempo médio de retenção e conversões por página.
3. **🎯 Leads & Conversões:** Lista detalhada de conversões com auditoria de integridade de tráfego pago (OK, Aviso ou Crítico), modal com a jornada completa do visitante e botão de exportação em CSV (compatível com Excel via UTF-8 BOM e delimitador `;`).
4. **🌐 Origem & Campanhas:** Relatório detalhado cruzando `utm_source`, `utm_medium`, `utm_campaign`, `gclid` e `fbclid`.
5. **🛤️ Funis & Atribuição:** Funil de 4 etapas (Visualização de Página ➔ Rolagem Profunda ➔ Intenção/Formulário ➔ Conversão Confirmada) com taxas de abandono; Análise de Atribuição Multitouch comparando *First Touch* vs *Last Touch*.
6. **⚡ DebugView (Ao Vivo):** Transmissão de eventos em tempo real via Server-Sent Events (SSE), linha do tempo cronológica, filtros por tipo e origem (Produção vs Debug), simulador de eventos de teste sintéticos e limpeza de buffer.
7. **🚀 Envios & CAPI (Wizard):** Assistente passo a passo para configuração de Meta CAPI, Google Ads, GA4 MP, LinkedIn CAPI e Webhook CRM, com botões de teste (*Ping*) e diagnósticos semânticos em tempo real.
8. **📚 Guia de Instalação SDK:** Manual interativo de implementação, snippets prontos para GTM, mapeamento de eventos e guia da API `window.hnTrack`.
9. **⚙️ Configurações & Domínios:** Gestão de Chaves de API com telemetria e revogação imediata, whitelist de domínios permitidos e central de alertas de segurança com aprovação em 1 clique.

---

## 💻 Instalação da Tag no Site do Cliente

Adicione o script no `<head>` ou no final do `<body>` do site:

```html
<!-- HN Performance Server-Side Tracking Tag -->
<script 
  src="https://trackeamento.hnperformancedigital.com.br/sdk/tracker.js" 
  data-site-key="hn_live_key_0123456789abcdef01234567" 
  async>
</script>
```

### Disparo Manual via JavaScript:
```javascript
// Exemplo: Disparo de Conversão Qualificada
window.hnTrack('purchase', {
  user_data: {
    email: 'cliente@exemplo.com.br',
    phone: '21999998888',
    name: 'Nome do Cliente'
  },
  custom_data: {
    value: 197.00,
    currency: 'BRL',
    order_id: 'PED-12345'
  }
});
```

### Gestão de Consentimento LGPD (Integração com CMPs):
```javascript
// Disparado imediatamente após a escolha do usuário no banner de cookies
window.hnTrack('consent', {
  necessary: true,
  analytics: true,
  marketing: false // Desativa persistência de UTMs e despacho para Meta/Google
});
```

### Marcação Explícita de Formulários (Opcional):
```html
<input type="email" data-hn-field="email" placeholder="Seu melhor e-mail">
<input type="tel" data-hn-field="phone" placeholder="Seu WhatsApp">
<input type="text" data-hn-field="name" placeholder="Seu nome completo">
```

---

## 🛠️ Stack Tecnológica

| Componente | Tecnologia | Versão | Função Principal |
| :--- | :--- | :--- | :--- |
| **Linguagem** | Go (Golang) | 1.24+ | Backend compilado de ultra-alta performance e concorrência nativa. |
| **Framework Web** | Fiber v2 / fasthttp | v2.52 | Roteamento HTTP com latência sub-milissegundo e baixo consumo de memória. |
| **Banco Analítico** | ClickHouse Server | 24+ | Banco de dados colunar OLAP para agregação de milhões de eventos com compressão extrema. |
| **Banco Relacional** | PostgreSQL | 16+ | Metadados transacionais, usuários, sessões, chaves de API e grafo de identidade. |
| **Fila / Streaming** | Redis | 7+ | Ingestão bufferizada via Redis Streams (`stream:events:raw`) e Pub/Sub em tempo real. |
| **Proxy Reverso** | Traefik | v3 | Roteamento de borda com geração e renovação automática de certificados SSL Let's Encrypt. |
| **SDK Frontend** | Vanilla JavaScript | ES5/ES6 | Script ultraleve (< 4 KB), sem dependências externas, compatível com GTM. |
| **Containers** | Docker & Compose | 2+ | Empacotamento unificado de microsserviços. |

---

## 🚀 Inicialização Rápida em Desenvolvimento

1. **Clone o repositório e configure as variáveis de ambiente:**
   ```bash
   cp .env.example .env
   ```

2. **Inicie os serviços via Docker Compose:**
   ```bash
   docker compose up -d --build
   ```

3. **Acesse as interfaces da aplicação:**
   - **Status da API:** [http://localhost:8080/health](http://localhost:8080/health)
   - **Dashboard Administrativo:** [http://localhost:8080/dashboard](http://localhost:8080/dashboard) (Login padrão: `admin` / senha configurada no `.env`)
   - **Guia do SDK:** [http://localhost:8080/docs](http://localhost:8080/docs)
   - **Script do SDK:** [http://localhost:8080/sdk/tracker.js](http://localhost:8080/sdk/tracker.js)

4. **Executar a suíte de testes automatizados:**
   ```bash
   go test -v -race ./...
   ```

---

## 📖 Documentação Complementar

- [Manual Técnico e Guia de Verificação Interna (Auditoria e Telas)](docs/MANUAL_DE_VERIFICACAO_INTERNA.md)
- [Especificação de Governança de Dados e Privacidade LGPD / ANPD](docs/PRIVACY_AND_DATA_GOVERNANCE.md)
- [Guia Operacional e Contexto de Arquitetura para Agentes de IA](docs/CONTEXTO_AGENTES.md)
- [Guia e Procedimentos de Deploy em Produção](DEPLOY.md)

---

## 📄 Licença

Distribuído sob a licença **MIT**. Consulte `LICENSE` para mais detalhes.
