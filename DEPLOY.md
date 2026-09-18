# Guia de Deploy & Operação: HN Server-Side Tracking Engine

Este documento descreve como subir, configurar e operar a infraestrutura de rastreamento server-side em Go com Docker, Traefik, ClickHouse, PostgreSQL e Redis na VPS.

---

## 1. Estrutura e Pré-requisitos na VPS

- **Servidor:** VPS (`vps.griddmkt360.com.br` ou IP dedicado).
- **Portas:** 80 (HTTP) e 443 (HTTPS) liberadas para o Traefik.
- **DNS:** Subdomínio apontado via Entrada **A** para o IP da VPS (ex: `track.hnperformancedigital.com.br`).

---

## 2. Passo a Passo de Instalação e Execução

### Passo 1: Clonar ou Copiar o Repositório na VPS
```bash
cd /opt
git clone <repo-url> trackeamento-hn
cd trackeamento-hn
```

### Passo 2: Configurar o Arquivo `.env`
Copie o arquivo de exemplo e defina suas senhas e domínio de produção:
```bash
cp .env.example .env
nano .env
```
Verifique as variáveis fundamentais:
- `TRACKING_DOMAIN=track.hnperformancedigital.com.br`
- `ACME_EMAIL=admin@hnperformancedigital.com.br`
- `POSTGRES_PASSWORD=...`
- `CLICKHOUSE_PASSWORD=...`
- `REDIS_PASSWORD=...`

### Passo 3: Inicializar a Stack Docker
Execute:
```bash
docker compose up -d --build
```
Isso irá construir e subir:
1. `traefik`: Proxy reverso com SSL Let's Encrypt automático.
2. `redis`: Buffer de fila em tempo real (Streams).
3. `postgres`: Banco relacional e grafo de identidades (executa as migrations em `migrations/postgres`).
4. `clickhouse`: Banco analítico colunar (executa DDL em `migrations/clickhouse`).
5. `api`: Coletor HTTP em Go (`POST /api/v1/collect` e `/sdk/tracker.js`).
6. `ingester`: Motor de lote (batching) gravando a cada 2.000 eventos ou 2 segundos no ClickHouse.
7. `dispatcher`: Worker pool assíncrono enviando dados para Meta CAPI, Google Ads, GA4 e Webhook CRM.

### Passo 4: Verificar Logs de Inicialização
```bash
docker compose logs -f api ingester dispatcher
```

---

## 3. Instalação da Tag nos Sites dos Clientes

Adicione a tag `<script>` no `<head>` ou início do `<body>` do site cliente:

```html
<!-- HN Performance Server-Side Tracking -->
<script 
  src="https://track.hnperformancedigital.com.br/sdk/tracker.js" 
  data-site-key="hn_live_key_998877665544332211" 
  async>
</script>
```

### O que o script faz automaticamente:
1. **PageView**: Disparado imediatamente com URL, Referrer e parâmetros de campanha.
2. **Atribuição First-Touch**: Armazena no `localStorage` os parâmetros `utm_*`, `gclid`, `fbclid`, `ttclid` para mantê-los se o visitante navegar entre páginas.
3. **Cliques de WhatsApp**: Monitora cliques em botões com links `wa.me`, `api.whatsapp.com` e dispara evento `whatsapp_click`.
4. **Formulários de Lead**: Intercepta submissões normais de formulários, extrai os campos de e-mail/telefone e dispara evento `lead` via `navigator.sendBeacon`.

---

## 4. Disparo Manual de Eventos via JavaScript (Opcional)

Se o site utilizar funis com botões customizados ou checkout, pode-se usar:

```javascript
window.hnTrack('purchase', {
  user_data: {
    email: 'cliente@dominio.com',
    phone: '21999998888',
    name: 'Carlos Oliveira'
  },
  custom_data: {
    value: 497.00,
    currency: 'BRL',
    order_id: 'PED-10293'
  }
});
```

---

## 5. Consultas Analíticas no ClickHouse

Para consultar métricas de conversão e eventos agregados com velocidade em sub-segundo:

```sql
-- 1. Contagem de eventos por campanha nos últimos 30 dias
SELECT 
    utm_campaign, 
    event_name, 
    count(*) AS total_eventos,
    uniqExact(visitor_id) AS visitantes_unicos
FROM tracking_events.events
WHERE event_time >= now() - INTERVAL 30 DAY
GROUP BY utm_campaign, event_name
ORDER BY total_eventos DESC;

-- 2. Jornada dos últimos leads gerados
SELECT 
    event_time,
    visitor_id,
    gclid,
    fbclid,
    utm_source,
    landing_page
FROM tracking_events.events
WHERE event_name = 'lead'
ORDER BY event_time DESC
LIMIT 50;
```
