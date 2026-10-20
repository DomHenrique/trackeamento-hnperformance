# Guia de Reprocessamento Retroativo de Leads & Janelas de Aceitação

Este documento estabelece as diretrizes e limites técnicos para o reprocessamento seguro e retroativo de conversões de formulário e leads no **Trackeamento HN**, garantindo conformidade com as regras de atribuição das plataformas de anúncios e com a **LGPD (Lei Geral de Proteção de Dados)**.

---

## 1. Janelas de Aceitação Retroativa por Plataforma

Cada plataforma de anúncios (Ad Network) possui janelas temporais estritas para aceitação e atribuição de eventos offline ou retroativos em suas APIs de Conversão (CAPI). Eventos enviados fora dessas janelas são descartados ou categorizados como sem atribuição.

| Plataforma | API / Protocolo | Janela Retroativa Máxima | Identificadores Aceitos | Impacto de Envio Fora da Janela |
| :--- | :--- | :--- | :--- | :--- |
| **Meta Ads (Facebook/Instagram)** | Meta Conversions API (Graph v20+) | **7 dias** (`event_time` <= 7 dias atrás) | `em` (SHA256), `ph` (SHA256), `fbp`, `fbc` | Eventos com mais de 7 dias são **rejeitados com erro 400** (`subcode 2804003: event_time is older than 7 days`). |
| **LinkedIn Ads** | LinkedIn Conversions API (CAPI v2) | **90 dias** (`conversionHappenedAt`) | `SHA256_EMAIL`, `SHA256_PHONE` | Eventos com mais de 90 dias não são computados para otimização de campanhas. Requer **obrigatoriamente** e-mail ou fone. |
| **Google Ads** | Google Ads API / Enhanced Conversions / Data Manager | **90 dias** (recomendado: até **30 dias**) | `hashedEmail`, `hashedPhoneNumber`, `gclid` | Eventos após 90 dias perdem o vínculo com cliques (`gclid`) e com Enhanced Conversions para smart bidding. |
| **TikTok Ads** | TikTok Events API | **7 dias** (`timestamp`) | `email_hash`, `phone_hash`, `ttclid` | Descarte silencioso ou erro 40000 para eventos anteriores a 7 dias. |

> [!IMPORTANT]
> **Recomendação Operacional:** Sempre que houver necessidade de reprocessar leads de campanhas (ex: após correções de captura em builders ou manutenções de webhook), execute o reprocessamento no menor intervalo possível, preferencialmente dentro de **48 horas**, para maximizar o índice de correspondência (Match Rate) do Meta Ads e do LinkedIn CAPI.

---

## 2. Requisitos de Governança e LGPD

1. **Consentimento Explícito Obrigatório:** Não é permitido reprocessar leads cujo consentimento de marketing tenha sido negado no momento da captura original. O utilitário exige `--explicit-consent=true`.
2. **Minimização de Dados e Purga Transitória:** 
   - Apenas hashes criptográficos SHA-256 (`Normalized & Lowercased`) são transmitidos externamente às plataformas.
   - O registro temporário criado no Redis possui TTL estrito de 15 minutos e é purgado logo após o término dos despachos ativos.
3. **Impedimento de Duplicação de Conversão:** O `submission_id` fornecido ou gerado garante que o lead não conte como duas conversões diferentes caso a plataforma já o tenha registrado.

---

## 3. Como Usar o Utilitário de Reprocessamento

O utilitário está localizado em `cmd/tools/reprocess_lead`:

### Compilação Local
```bash
go build -o bin/reprocess_lead ./cmd/tools/reprocess_lead
```

### Sintaxe Básica
```bash
./bin/reprocess_lead \
  --site-id "<UUID_DO_SITE>" \
  --email "lead@dominio.com.br" \
  --phone "5554991143857" \
  --name "Nome do Lead" \
  --occurred-at "2026-10-18T14:30:00Z" \
  --dry-run
```

### Flags Disponíveis

| Flag | Tipo | Padrão | Descrição |
| :--- | :--- | :--- | :--- |
| `--site-id` | string | *obrigatório* | UUID do site no Trackeamento HN. |
| `--email` | string | `""` | E-mail do lead (normalizado e hasheado automaticamente). |
| `--phone` | string | `""` | Telefone do lead (com DDI e DDD). |
| `--name` | string | `""` | Nome completo do lead. |
| `--submission-id` | string | auto-gerado | ID original da submissão para reconciliação e deduplicação. |
| `--event-name` | string | `"form_submit"` | Nome do evento de conversão. |
| `--lifecycle-state`| string | `"form_submit_success"` | Estado de ciclo de vida do formulário. |
| `--occurred-at` | string | agora | Data/hora do evento no formato RFC3339 (ex: `2026-10-18T14:30:00Z`). |
| `--dry-run` | bool | `false` | Simula a normalização, valida janelas e não despacha nada. |
| `--queue` | bool | `true` | Enfileira no stream do Redis (`stream:events:dispatch`) para consumo assíncrono. |
| `--explicit-consent`| bool| `true` | Validação de consentimento sob a LGPD. |

### Exemplo de Reprocessamento E2E (SPS Power)
```bash
./bin/reprocess_lead \
  --site-id "a0000000-0000-0000-0000-000000000001" \
  --submission-id "sub_sps_eduardo_rosalem" \
  --email "eduardorosalem@soprano.com.br" \
  --phone "5554991143857" \
  --name "Eduardo Rosalem" \
  --occurred-at "2026-10-18T14:30:00Z"
```

O comando irá:
1. Validar as janelas de Meta, LinkedIn e Google Ads.
2. Normalizar e calcular hashes SHA-256 de e-mail e telefone.
3. Salvar o `LeadCacheRecord` com TTL de 15 minutos no Redis.
4. Enfileirar o evento no `stream:events:dispatch`.
5. O `WorkerPool` despachará para todas as integrações ativas (LinkedIn CAPI, Meta CAPI e Google Ads) com deduplicação por `submission_id` e purga imediata do cache pós-despacho.
