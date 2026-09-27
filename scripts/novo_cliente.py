#!/usr/bin/env python3
"""
Provisionador Inteligente de Nova Instância de Cliente (Multi-Tenant Segregado)
- Busca automaticamente portas livres no host (Postgres, Redis, ClickHouse).
- Varre arquivos .env.* existentes para garantir que instâncias paradas não sofram colisão.
- Gera senhas criptográficas e tokens únicos para cada cliente.
- Cria o arquivo de configuração .env.<cliente>.
- Suporta inicialização automática via flag --up.
"""

import os
import sys
import glob
import socket
import secrets
import argparse
import subprocess
from pathlib import Path

# Portas padrão de partida para instâncias adicionais
BASE_PORTS = {
    "POSTGRES": 15432,
    "REDIS": 16379,
    "CLICKHOUSE_HTTP": 18123,
    "CLICKHOUSE_NATIVE": 19000,
}

def is_port_in_use(port: int) -> bool:
    """Verifica se a porta está em uso no sistema operacional."""
    for host in ('127.0.0.1', '0.0.0.0'):
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
            s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            try:
                s.bind((host, port))
            except OSError:
                return True
    return False

def get_reserved_ports_from_env_files(root_dir: Path) -> set:
    """Extrai portas já alocadas em arquivos .env.* no diretório."""
    reserved = set()
    env_files = list(root_dir.glob(".env*"))
    for ef in env_files:
        if ef.name in [".env.example", ".env.client.example"]:
            continue
        try:
            with open(ef, "r", encoding="utf-8") as f:
                for line in f:
                    line = line.strip()
                    if not line or line.startswith("#"):
                        continue
                    if any(key in line for key in ["_PORT=", "REDIS_HOST_PORT=", "POSTGRES_HOST_PORT="]):
                        parts = line.split("=", 1)
                        if len(parts) == 2 and parts[1].strip().isdigit():
                            reserved.add(int(parts[1].strip()))
        except Exception:
            pass
    return reserved

def find_next_free_port(start_port: int, reserved_ports: set) -> int:
    """Encontra a próxima porta livre que não esteja em uso nem reservada em outros .env."""
    port = start_port
    while port < 65535:
        if port not in reserved_ports and not is_port_in_use(port):
            reserved_ports.add(port)
            return port
        port += 1
    raise RuntimeError(f"Nenhuma porta livre encontrada a partir de {start_port}")

def generate_random_password(length: int = 20) -> str:
    return secrets.token_urlsafe(length)[:length]

def generate_hex_key(bytes_len: int = 16) -> str:
    return secrets.token_hex(bytes_len)

def main():
    parser = argparse.ArgumentParser(
        description="Provisiona uma nova instância segregada para um cliente com portas automáticas."
    )
    parser.add_argument("slug", nargs="?", help="Identificador único do cliente (ex: cliente2, monica, kastello)")
    parser.add_argument("domain", nargs="?", help="Subdomínio CNAME de rastreamento (ex: track.cliente.com.br)")
    parser.add_argument("--tag", default="latest", help="Tag da imagem Docker no Docker Hub (padrão: latest)")
    parser.add_argument("--email", default="admin@hnperformancedigital.com.br", help="Email para o Let's Encrypt")
    parser.add_argument("--up", action="store_true", help="Sobe a stack imediatamente com docker compose")
    
    args = parser.parse_args()
    
    root_dir = Path(__file__).resolve().parent.parent

    # Solicita dados interativamente se não fornecidos
    slug = args.slug
    if not slug:
        slug = input("👉 Digite o identificador único do cliente (slug, ex: cliente_abc): ").strip()
    
    slug = slug.lower().replace("-", "_").replace(" ", "_")
    if not slug:
        print("❌ Erro: O slug do cliente não pode ser vazio.")
        sys.exit(1)
        
    domain = args.domain
    if not domain:
        domain = input("👉 Digite o subdomínio CNAME do cliente (ex: track.cliente.com.br): ").strip()
        
    if not domain:
        print("❌ Erro: O subdomínio de rastreamento não pode ser vazio.")
        sys.exit(1)

    print(f"\n🔍 Analisando portas disponíveis no servidor para '{slug}'...")
    reserved = get_reserved_ports_from_env_files(root_dir)

    # Aloca portas inteligentes
    pg_port = find_next_free_port(BASE_PORTS["POSTGRES"], reserved)
    redis_port = find_next_free_port(BASE_PORTS["REDIS"], reserved)
    ch_http_port = find_next_free_port(BASE_PORTS["CLICKHOUSE_HTTP"], reserved)
    ch_native_port = find_next_free_port(BASE_PORTS["CLICKHOUSE_NATIVE"], reserved)

    print(f"  ✅ PostgreSQL:       Porta {pg_port} (livre)")
    print(f"  ✅ Redis:            Porta {redis_port} (livre)")
    print(f"  ✅ ClickHouse HTTP:  Porta {ch_http_port} (livre)")
    print(f"  ✅ ClickHouse TCP:   Porta {ch_native_port} (livre)")

    # Gera credenciais fortes
    admin_pass = generate_random_password(18)
    server_key = generate_hex_key(16)
    hmac_pepper = generate_hex_key(16)
    redis_pass = generate_random_password(16)
    pg_pass = generate_random_password(16)
    ch_pass = generate_random_password(16)

    env_content = f"""# ==============================================================================
# Configuração de Ambiente para Cliente: {slug}
# Gerado automaticamente pelo assistente de provisionamento
# ==============================================================================

# Identificador exclusivo do cliente
CLIENT_SLUG={slug}
TAG={args.tag}

# Ambiente e Subdomínio First-Party
ENV=production
TRACKING_DOMAIN={domain}
ACME_EMAIL={args.email}
HTTP_PORT=8080

# Portas no Host Segregadas (Calculadas dinamicamente sem conflito)
POSTGRES_HOST_PORT={pg_port}
REDIS_HOST_PORT={redis_port}
CLICKHOUSE_HTTP_HOST_PORT={ch_http_port}
CLICKHOUSE_NATIVE_HOST_PORT={ch_native_port}

# Rede do Traefik existente
TRAEFIK_NETWORK=tracking-net

# Credenciais do Administrador Mestre deste Cliente
ADMIN_USER=admin
ADMIN_PASSWORD={admin_pass}

# Chaves de Criptografia Interna
SERVER_API_KEY={server_key}
HMAC_PEPPER={hmac_pepper}

# Redis Interno do Cliente
REDIS_ADDR={slug}-redis:6379
REDIS_PASSWORD={redis_pass}
REDIS_STREAM_RAW=stream:events:raw
REDIS_STREAM_DISPATCH=stream:events:dispatch
REDIS_CONSUMER_GROUP=tracking_workers

# PostgreSQL Interno do Cliente
POSTGRES_HOST={slug}-postgres
POSTGRES_PORT=5432
POSTGRES_DB=tracking_db
POSTGRES_USER=tracking_user
POSTGRES_PASSWORD={pg_pass}
POSTGRES_SSLMODE=disable

# ClickHouse Interno do Cliente
CLICKHOUSE_ADDR={slug}-clickhouse:9000
CLICKHOUSE_DB=tracking_events
CLICKHOUSE_USER=default
CLICKHOUSE_PASSWORD={ch_pass}

# Ingester Batching Settings
INGESTER_BATCH_SIZE=2000
INGESTER_FLUSH_INTERVAL_SEC=2

# Dispatcher Worker Settings
DISPATCHER_WORKERS=8
DISPATCHER_MAX_RETRIES=5
DISPATCHER_TIMEOUT_SEC=10

# Bootstrap Declarativo Opcional
SEED_DEFAULT_SITE=false
DEFAULT_CLIENT_NAME={slug.capitalize()}
DEFAULT_SITE_NAME=Site Principal
DEFAULT_SITE_DOMAIN={domain}

# Proxies Confiáveis (Prevenção contra IP Spoofing)
TRUSTED_PROXIES=127.0.0.1,172.16.0.0/12,10.0.0.0/8
"""

    target_env_file = root_dir / f".env.{slug}"
    with open(target_env_file, "w", encoding="utf-8") as f:
        f.write(env_content)

    print(f"\n🎉 Arquivo gerado com sucesso: .env.{slug}")
    print("=" * 65)
    print("📋 DADOS DE ACESSO DO CLIENTE:")
    print(f"   • Subdomínio:    https://{domain}")
    print(f"   • Painel Admin:  https://{domain}/admin")
    print(f"   • Usuário:       admin")
    print(f"   • Senha:         {admin_pass}")
    print("=" * 65)
    print("🔌 PORTAS ISOLADAS NO HOST (DBeaver / Ferramentas Locais):")
    print(f"   • PostgreSQL:    127.0.0.1:{pg_port}")
    print(f"   • Redis:         127.0.0.1:{redis_port}")
    print(f"   • ClickHouse:    127.0.0.1:{ch_http_port} (HTTP) / {ch_native_port} (Native)")
    print("=" * 65)

    compose_cmd = [
        "docker", "compose",
        "-p", slug,
        "-f", "docker-compose.client.yml",
        "--env-file", f".env.{slug}",
        "up", "-d"
    ]
    cmd_str = " ".join(compose_cmd)

    if args.up:
        print(f"\n🚀 Subindo stack do cliente '{slug}'...")
        subprocess.run(compose_cmd, cwd=root_dir, check=True)
        print(f"\n✅ Stack '{slug}' iniciada com sucesso!")
    else:
        print(f"\n💡 Para iniciar este cliente na VPS, execute:")
        print(f"   {cmd_str}\n")

if __name__ == "__main__":
    main()
