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

## 3. Deploy Automatizado da Nova Aplicação na VPS (Com Detecção Automática de Portas)

Para evitar qualquer conflito caso você tenha **2, 3 ou dezenas de clientes instalados no mesmo servidor**, criamos o provisionador automático `scripts/novo_cliente.sh`.

Ele:
1. Varre o servidor e encontra a próxima porta livre para PostgreSQL (15432...), Redis (16379...) e ClickHouse (18123... / 19000...).
2. Analisa arquivos `.env.*` já existentes na pasta para garantir que mesmo instâncias temporariamente paradas não sofram colisão de portas.
3. Gera senhas e chaves criptográficas fortes exclusivas para o cliente.
4. Gera o arquivo `.env.<cliente>` pronto e validado.

### Passo 3.1: Conectar na VPS
```bash
ssh -i ~/.gemini/id_rsa.1731418096 root@vps.griddmkt360.com.br
```

### Passo 3.2: Provisionar Novo Cliente (Exemplo com 1 comando)
No diretório do projeto na VPS:
```bash
./scripts/novo_cliente.sh cliente2 track.cliente2.com.br
```

Saída de exemplo:
```text
🔍 Analisando portas disponíveis no servidor para 'cliente2'...
  ✅ PostgreSQL:       Porta 15432 (livre)
  ✅ Redis:            Porta 16379 (livre)
  ✅ ClickHouse HTTP:  Porta 18123 (livre)
  ✅ ClickHouse TCP:   Porta 19000 (livre)

🎉 Arquivo gerado com sucesso: .env.cliente2
```

Se você rodar novamente para um 3º ou 4º cliente:
```bash
./scripts/novo_cliente.sh cliente3 track.cliente3.com.br
```
O script detecta automaticamente que as portas anteriores estão reservadas e avança para `15433`, `16380`, `18124`, `19001`!

### Passo 3.3: Subir a Stack
Você pode subir com o comando impresso pelo script:
```bash
docker compose -p cliente2 -f docker-compose.client.yml --env-file .env.cliente2 up -d
```
*(Ou passar a flag `--up` diretamente no script: `./scripts/novo_cliente.sh cliente2 track.cliente2.com.br --up`)*.

### Passo 3.4: Verificar Status e Logs
```bash
docker compose -p cliente2 -f docker-compose.client.yml ps
docker compose -p cliente2 -f docker-compose.client.yml logs -f api
```

O Traefik detectará automaticamente o container `cliente2-api`, gerará o certificado SSL para `track.cliente2.com.br` e iniciará o roteamento seguro.

