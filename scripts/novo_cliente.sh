#!/usr/bin/env bash
set -e

# ==============================================================================
# Wrapper para o Provisionador de Novo Cliente
# Uso: ./scripts/novo_cliente.sh <slug_cliente> <subdominio> [--up]
# ==============================================================================

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
python3 "${DIR}/novo_cliente.py" "$@"
