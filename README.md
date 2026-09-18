# 🚀 HN Server-Side Tracking Engine

Plataforma de alta escala para **rastreamento server-side e atribuição multitouch** de marketing digital desenvolvida em **Go (Golang)**, com armazenamento analítico colunar em **ClickHouse**, banco relacional transacional em **PostgreSQL**, buffer de ingestão em **Redis Streams** e terminação TLS via **Traefik**.

Desenvolvido para contornar restrições de bloqueadores de anúncios (AdBlockers), Apple ITP (iOS 14.5+) e assegurar **100% de integridade na atribuição de primeiro clique** (Google Ads, Meta Ads, TikTok) até a conversão final no CRM/WhatsApp.

---

## 🏛️ Arquitetura do Sistema

```
                    INTERNET / CLIENT SITES
                              │
                              ▼
                       ┌─────────────┐
                       │   Traefik   │ (SSL Let's Encrypt / Proxy Reverso)
                       └──────┬──────┘
                              │
                              ▼
                       ┌─────────────┐
                       │   Go API    │ (Coletor HTTP sub-milissegundo)
                       │  Tracking   │ (Set-Cookie _vid 1st-party)
                       └──────┬──────┘
                              │
                              ▼
                       ┌─────────────┐
                       │    Redis    │ (Stream / Buffer de Ingestão)
                       └──────┬──────┘
                              │
                 ┌────────────┴────────────┐
                 ▼                         ▼
      ┌─────────────────────┐   ┌─────────────────────┐
      │  Go Batch Ingester  │   │    Go Dispatcher    │
      └──────────┬──────────┘   └──────────┬──────────┘
                 │                         │
        ┌────────┴────────┐      ┌─────────┼─────────┐
        ▼                 ▼      ▼         ▼         ▼
  ┌───────────┐    ┌──────────┐ ┌────┐  ┌──────┐  ┌─────┐
  │PostgreSQL │    │ClickHouse│ │Meta│  │Google│  │ CRM │
  │ Metadados │    │ Eventos  │ │CAPI│  │ Ads  │  │ n8n │
  │& Identidad│    │ (OLAP)   │ └────┘  └──────┘  └─────┘
  └───────────┘    └──────────┘
```

---

## ✨ Principais Funcionalidades

1. **API de Coleta Ultra-Rápida (`cmd/api`)**:
   - Responde em tempo sub-milissegundo com status `HTTP 204 No Content`.
   - Gerencia cookie 1st-party `_vid` (validade de 1 ano, política `SameSite=Lax`).
   - Extrai IP real (`CF-Connecting-IP`, `X-Real-IP`, `X-Forwarded-For`), User-Agent e parâmetros de anúncio (`gclid`, `gbraid`, `wbraid`, `fbclid`, `ttclid`, `utm_*`).

2. **Ingestão em Lote no ClickHouse (`cmd/ingester`)**:
   - Acumulador em memória thread-safe.
   - Flush em batch a cada **2.000 eventos** ou **2 segundos**.
   - Tabela `events` com engine `MergeTree()` colunar, particionada por mês e indexada por `(site_id, event_name, event_time, visitor_id)`.

3. **Grafo de Identidade & Atribuição First-Touch (`internal/identity`)**:
   - Salva e protege a origem do primeiro contato no PostgreSQL.
   - Normalização automática de dados de contato com hash `SHA-256` para conformidade com LGPD e Meta CAPI.

4. **Dispatcher Server-Side Concorrente (`cmd/dispatcher`)**:
   - Conexões HTTP persistentes com pooling (keep-alive) e retry exponencial.
   - Integrações nativas:
     - **Meta Conversions API (CAPI)**: Envio com deduplicação (`event_id`), hashes e cookies `_fbp`/`_fbc`.
     - **Google Ads Offline / Enhanced Conversions**: Suporte a `gclid` e dados normalizados.
     - **GA4 Measurement Protocol**: Suporte a `/mp/collect` com `client_id`.
     - **Webhooks CRM / n8n**: Disparo enriquecido com toda a jornada de atribuição.

5. **SDK JavaScript Leve (`sdk/tracker.js`)**:
   - Script Vanilla JS minimalista (< 4KB gzipped), sem dependências.
   - Rastreamento automático de `page_view`, cliques em botões de WhatsApp e formulários.
   - Preservação de UTMs e Click IDs no `localStorage`.

---

## 💻 Instalação da Tag no Site

Basta adicionar a tag no `<head>` ou `<body>` do site:

```html
<script 
  src="https://trackeamento.hnperformancedigital.com.br/sdk/tracker.js" 
  data-site-key="hn_live_key_998877665544332211" 
  async>
</script>
```

### Disparo Manual via JS (Opcional):
```javascript
window.hnTrack('purchase', {
  user_data: {
    email: 'lead@exemplo.com.br',
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

---

## 🛠️ Stack Tecnológica

- **Linguagem:** Go (Golang 1.24+)
- **Framework Web:** Fiber v2 / fasthttp
- **Banco Analítico:** ClickHouse Server 24
- **Banco Relacional:** PostgreSQL 16
- **Fila / Streaming:** Redis 7 (Redis Streams)
- **Proxy Reverso & SSL:** Traefik v3 (Let's Encrypt automático)
- **Orquestração:** Docker & Docker Compose

---

## 📄 Licença

Distribuído sob a licença MIT. Consulte `LICENSE` para obter mais informações.
