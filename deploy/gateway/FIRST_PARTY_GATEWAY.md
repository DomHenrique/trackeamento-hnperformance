# First-Party Ingress Gateway (CNAME & On-Demand TLS)

Este documento descreve a arquitetura e operação do **First-Party Ingress Gateway** da HN Performance.

---

## 1. Como Funciona

Em vez do site do cliente enviar eventos para o domínio central `trackeamento.hnperformancedigital.com.br` (vulnerável a bloqueios de terceiros e ad-blockers), o cliente configura um subdomínio próprio em sua zona DNS:

- **Exemplo**: `track.spspower.com.br`
- **Tipo de Registro**: `CNAME`
- **Destino / Apontamento**: `vps.griddmkt360.com.br` (ou o IP `178.253.250.73`)

### Benefícios
1. **100% First-Party**: Para navegadores (Safari ITP, Firefox, Brave) e ad-blockers, a requisição é feita para o mesmo domínio ou subdomínio do site do cliente.
2. **Zero Dependência de Cookies**: A identidade é calculada deterministicamente no servidor Go via HMAC-SHA256, sem gravar nada no dispositivo do visitante.
3. **Imunidade a Bloqueadores**: Bloqueadores com regras estáticas não possuem o domínio do cliente em suas blacklists genéricas.

---

## 2. Emissão Automatizada de Certificados SSL (On-Demand TLS)

O reverse proxy Caddy instalado na VPS gerencia a emissão automática de certificados SSL via Let's Encrypt / ZeroSSL sob demanda:
1. Quando uma requisição HTTPS chega em `https://track.spspower.com.br`, o Caddy consulta o endpoint interno do coletor:
   `GET http://tracking_api:8080/api/v1/domains/check-cname-authorized?domain=track.spspower.com.br`
2. Se o subdomínio estiver cadastrado e verificado no banco de dados da HN Performance, o endpoint responde `HTTP 200 OK`.
3. O Caddy solicita e obtém o certificado SSL dinamicamente em segundos, sem necessidade de reiniciar nenhum container.

---

## 3. Roteamento por Host Header

O coletor Go (`internal/collector/handler.go`) lê o cabeçalho `Host` / `X-Forwarded-Host`. Se a requisição vier através de `track.spspower.com.br`, o coletor associa o evento automaticamente ao tenant SPS Power, mesmo que a tag script omita a chave pública `site_key`.
