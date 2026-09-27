#!/usr/bin/env bash
set -e

# ==============================================================================
# Script de Build e Push para Docker Hub
# Imagem: domhenrique/hn-tracking:<tag>
# ==============================================================================

REPO="domhenrique/hn-tracking"
TAG="${1:-latest}"
IMAGE_FULL="${REPO}:${TAG}"

echo "=========================================="
echo "🚀 Iniciando Build da imagem Docker..."
echo "📦 Repositório: ${REPO}"
echo "🏷️  Tag:         ${TAG}"
echo "🎯 Imagem:      ${IMAGE_FULL}"
echo "=========================================="

# Build da imagem unificada (com api, ingester e dispatcher)
docker build -t "${IMAGE_FULL}" .

if [ "${TAG}" != "latest" ]; then
    echo "🏷️  Criando tag adicional: ${REPO}:latest"
    docker tag "${IMAGE_FULL}" "${REPO}:latest"
fi

echo "=========================================="
echo "📤 Enviando imagem para o Docker Hub..."
echo "=========================================="

docker push "${IMAGE_FULL}"

if [ "${TAG}" != "latest" ]; then
    docker push "${REPO}:latest"
fi

echo "=========================================="
echo "✅ Imagem ${IMAGE_FULL} enviada com sucesso para o Docker Hub!"
echo "=========================================="
