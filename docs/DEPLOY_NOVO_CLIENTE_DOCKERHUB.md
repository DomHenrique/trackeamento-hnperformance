# Guia de Deploy Isolado para Novo Cliente via Docker Hub

Este guia descreve como publicar a imagem do motor de rastreamento no **Docker Hub** (`domhenrique/hn-tracking:<tag>`) e implantar uma **nova instância 100% isolada** para outro cliente na VPS, **sem tocar ou impactar a aplicação da SPS Power** (`trackeamento.hnperformancedigital.com.br`).

---

## 🛡️ Regra de Ouro: Isolamento Total da SPS Power

A SPS Power continua operando sem alterações na VPS com:
- Containers: `tracking_api`, `tracking_redis`, `tracking_postgres`, `tracking_clickhouse`, `tracking_ingester`, `tracking_dispatcher`
- Portas no host: `5432` (PG), `6379` (Redis), `8123` e `9000` (ClickHouse)
- Domínio: `trackeamento.hnperformancedigital.com.br`

Para o novo cliente, utilizamos:
- **Prefixo exclusivo** (`CLIENT_SLUG=nome_cliente`) em todos os containers e volumes.
- **Portas no host segregadas**: `15432` (PG), `16379` (Redis), `18123`/`19000` (ClickHouse).
- **Roteamento HTTPS via Traefik**: Utiliza a mesma rede de proxy (`tracking-net`), porém com regra de host dedicada para o subdomínio do novo cliente.

---

## 1. Login e Publicação no Docker Hub

### Passo 1.1: Autenticar no Docker Hub (Máquina Local)
No terminal local, execute o login com seu usuário do Docker Hub:
```bash
docker login -u domhenrique
```
*(Digite sua senha ou Personal Access Token do Docker Hub quando solicitado).*

### Passo 1.2: Fazer Build e Push da Imagem
Você pode utilizar o script automatizado criado no projeto:
```bash
./scripts/dockerhub_push.sh v1.0.0
```
Ou executar manualmente:
```bash
# 1. Build da imagem com todos os binários (api, ingester, dispatcher)
docker build -t domhenrique/hn-tracking:v1.0.0 -t domhenrique/hn-tracking:latest .

# 2. Envio para o Docker Hub
docker push domhenrique/hn-tracking:v1.0.0
docker push domhenrique/hn-tracking:latest
```

---

## 2. Preparação do DNS do Novo Cliente

Como você possui acesso ao DNS deste novo cliente:
1. No painel de DNS do cliente (ex: Cloudflare, Registro.br, GoDaddy):
   - **Tipo**: `CNAME`
   - **Nome / Host**: `dados` ou `track` (ex: `track.sitecliente.com.br`)
   - **Destino / Target**: `vps.griddmkt360.com.br` (ou IP direto se for registro A)
   - **Cloudflare**: Desative o proxy (Nuvem Cinza / **DNS Only**) para permitir que o Traefik emita o certificado Let's Encrypt automaticamente.

---

## 3. Deploy da Nova Aplicação na VPS

### Passo 3.1: Conectar na VPS
```bash
ssh -i ~/.gemini/id_rsa.1731418096 root@vps.griddmkt360.com.br
```

### Passo 3.2: Criar Diretório Isolado para o Cliente
```bash
mkdir -p /opt/tracking/clientes/cliente_x
cd /opt/tracking/clientes/cliente_x
```

### Passo 3.3: Copiar o Docker Compose e Configurar o `.env`
Copie o arquivo `docker-compose.client.yml` e o `.env.client.example` do repositório para o diretório do cliente:
```bash
# Criar o arquivo de variáveis do cliente:
cp .env.client.example .env.cliente_x
nano .env.cliente_x
```

Configure os campos essenciais:
```ini
CLIENT_SLUG=cliente_x
TAG=v1.0.0
TRACKING_DOMAIN=track.sitecliente.com.br
ACME_EMAIL=seu-email@dominio.com

# Credenciais do painel
ADMIN_USER=admin
ADMIN_PASSWORD=senha_forte_do_cliente_min_16_chars

# Senhas dos bancos isolados
REDIS_PASSWORD=senha_forte_redis
POSTGRES_PASSWORD=senha_forte_postgres
CLICKHOUSE_PASSWORD=senha_forte_clickhouse
```

### Passo 3.4: Subir a Stack
```bash
docker compose -p cliente_x -f docker-compose.client.yml --env-file .env.cliente_x up -d
```

### Passo 3.5: Verificar Status e Logs
```bash
docker compose -p cliente_x -f docker-compose.client.yml ps
docker compose -p cliente_x -f docker-compose.client.yml logs -f api
```

O Traefik detectará automaticamente o container `cliente_x-api`, gerará o certificado SSL para `track.sitecliente.com.br` e iniciará o roteamento seguro.
