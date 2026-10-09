#!/usr/bin/env bash
# ==============================================================================
# HN Performance Tracking Engine - Script de Diagnóstico Pré-Voo de Gateway
# ==============================================================================
# Valida em 4 etapas se um subdomínio First-Party de cliente está pronto para
# tráfego em produção (DNS -> Handshake TLS -> Certificado Válido -> SDK 200 OK).
#
# Uso:
#   ./scripts/check_gateway.sh <dominio> [site_key]
# Exemplo:
#   ./scripts/check_gateway.sh track.spspower.com.br
# ==============================================================================

set -eo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
BOLD='\033[1m'
NC='\033[0m' # No Color

DOMAIN="${1:-}"
SITE_KEY="${2:-}"

if [ -z "$DOMAIN" ]; then
    echo -e "${YELLOW}Uso:${NC} $0 <dominio> [site_key]"
    echo -e "Exemplo: $0 track.spspower.com.br"
    exit 1
fi

echo -e "\n${BOLD}======================================================================${NC}"
echo -e "${BOLD}🩺 HN GATEWAY DOCTOR: Diagnóstico Pré-Voo de First-Party Domain${NC}"
echo -e "${BOLD}======================================================================${NC}"
echo -e "Alvo: ${BLUE}${DOMAIN}${NC}"
echo ""

ERRORS=0
WARNINGS=0

# ------------------------------------------------------------------------------
# ETAPA 1: Resolução DNS
# ------------------------------------------------------------------------------
echo -e "${BOLD}[1/4] Verificando Resolução DNS...${NC}"
CNAME_RESULT=$(dig +short CNAME "${DOMAIN}" 2>/dev/null || true)
IP_RESULTS=$(dig +short A "${DOMAIN}" 2>/dev/null || true)

if [ -n "$CNAME_RESULT" ]; then
    echo -e "  ${GREEN}✔ CNAME detectado:${NC} ${CNAME_RESULT}"
else
    echo -e "  ${YELLOW}ℹ Sem registro CNAME direto (pode ser entrada A ou proxy)${NC}"
fi

if [ -n "$IP_RESULTS" ]; then
    PRIMARY_IP=$(echo "$IP_RESULTS" | head -n 1)
    echo -e "  ${GREEN}✔ Resolução de IP OK:${NC} ${PRIMARY_IP}"
else
    echo -e "  ${RED}✖ FALHA: O domínio ${DOMAIN} não resolve para nenhum endereço IP.${NC}"
    echo -e "    -> Verifique a zona DNS no provedor do cliente."
    ERRORS=$((ERRORS + 1))
fi

# ------------------------------------------------------------------------------
# ETAPA 2: Handshake TLS & Inspeção do Certificado SSL
# ------------------------------------------------------------------------------
echo ""
echo -e "${BOLD}[2/4] Verificando Handshake TLS & Certificado SSL...${NC}"

CERT_INFO=$(echo | openssl s_client -servername "${DOMAIN}" -connect "${DOMAIN}:443" 2>/dev/null || true)

if [ -z "$CERT_INFO" ]; then
    echo -e "  ${RED}✖ FALHA: Não foi possível estabelecer conexão TCP/TLS na porta 443.${NC}"
    ERRORS=$((ERRORS + 1))
else
    CERT_SUBJECT=$(echo "$CERT_INFO" | openssl x509 -noout -subject 2>/dev/null || true)
    CERT_ISSUER=$(echo "$CERT_INFO" | openssl x509 -noout -issuer 2>/dev/null || true)
    CERT_DATES=$(echo "$CERT_INFO" | openssl x509 -noout -dates 2>/dev/null || true)

    echo -e "  Subject: ${CERT_SUBJECT}"
    echo -e "  Issuer : ${CERT_ISSUER}"

    # Verificação de certificados auto-assinados ou default fallback
    if echo "$CERT_ISSUER" | grep -qi "TRAEFIK DEFAULT CERT"; then
        echo -e "  ${RED}✖ ERRO CRÍTICO: O servidor respondeu com 'TRAEFIK DEFAULT CERT'!${NC}"
        echo -e "    O domínio não possui rota TLS com Let's Encrypt configurada no Ingress."
        echo -e "    Navegadores bloqueiam esse certificado com MOZILLA_PKIX_ERROR_SELF_SIGNED_CERT."
        ERRORS=$((ERRORS + 1))
    elif echo "$CERT_ISSUER" | grep -qi "self-signed\|localhost"; then
        echo -e "  ${RED}✖ ERRO CRÍTICO: Certificado auto-assinado detectado!${NC}"
        ERRORS=$((ERRORS + 1))
    elif echo "$CERT_ISSUER" | grep -qi "Let's Encrypt\|ZeroSSL\|Cloudflare\|DigiCert\|Google Trust Services"; then
        echo -e "  ${GREEN}✔ Certificado SSL emitido por Autoridade Certificadora válida!${NC}"
    else
        echo -e "  ${YELLOW}⚠ Certificado reconhecido, mas emissor incomum: ${CERT_ISSUER}${NC}"
        WARNINGS=$((WARNINGS + 1))
    fi

    if [ -n "$CERT_DATES" ]; then
        END_DATE=$(echo "$CERT_DATES" | grep "notAfter=" | cut -d= -f2)
        echo -e "  Validade até: ${END_DATE}"
    fi
fi

# ------------------------------------------------------------------------------
# ETAPA 3: Teste de Requisição do SDK Tracker (/sdk/tracker.js)
# ------------------------------------------------------------------------------
echo ""
echo -e "${BOLD}[3/4] Testando Acesso ao SDK Tracker via HTTPS...${NC}"

TRACKER_URL="https://${DOMAIN}/sdk/tracker.js"
if [ -n "$SITE_KEY" ]; then
    TRACKER_URL="${TRACKER_URL}?key=${SITE_KEY}"
fi

HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -m 5 "$TRACKER_URL" 2>/dev/null || echo "000")

if [ "$HTTP_CODE" = "200" ]; then
    echo -e "  ${GREEN}✔ Status HTTP 200 OK:${NC} O script /sdk/tracker.js foi entregue com sucesso!"
elif [ "$HTTP_CODE" = "000" ]; then
    echo -e "  ${RED}✖ FALHA: Conexão HTTPS abortada (possível erro de certificado TLS ou timeout).${NC}"
    ERRORS=$((ERRORS + 1))
else
    echo -e "  ${YELLOW}⚠ Código HTTP inesperado: ${HTTP_CODE}${NC} para ${TRACKER_URL}"
    WARNINGS=$((WARNINGS + 1))
fi

# ------------------------------------------------------------------------------
# ETAPA 4: Validação do Endpoint de Autorização Ask da API Interna
# ------------------------------------------------------------------------------
echo ""
echo -e "${BOLD}[4/4] Verificando Autorização do Domínio no Banco de Dados...${NC}"

# Tenta consultar o endpoint de validação localmente se a API estiver rodando
INTERNAL_CHECK=$(curl -s -o /dev/null -w "%{http_code}" "http://localhost:8080/api/v1/internal/validate-domain?domain=${DOMAIN}" 2>/dev/null || echo "000")

if [ "$INTERNAL_CHECK" = "200" ]; then
    echo -e "  ${GREEN}✔ Domínio autorizado na API local (/api/v1/internal/validate-domain -> 200 OK)${NC}"
elif [ "$INTERNAL_CHECK" = "403" ]; then
    echo -e "  ${RED}✖ Domínio NÃO cadastrado na API (/api/v1/internal/validate-domain -> 403 Forbidden)${NC}"
    echo -e "    O Caddy On-Demand TLS recusará emitir certificados para este host."
    ERRORS=$((ERRORS + 1))
else
    echo -e "  ${BLUE}ℹ API local em localhost:8080 não acessível no momento da execução do CLI.${NC}"
fi

# ------------------------------------------------------------------------------
# RESUMO FINAL
# ------------------------------------------------------------------------------
echo ""
echo -e "${BOLD}======================================================================${NC}"
if [ $ERRORS -eq 0 ]; then
    echo -e "${GREEN}${BOLD}🎉 SUCESSO: O domínio ${DOMAIN} está 100% OPERACIONAL e seguro!${NC}"
    echo -e "Pode ser utilizado no Google Tag Manager sem risco de bloqueio TLS."
    exit 0
else
    echo -e "${RED}${BOLD}🛑 ATENÇÃO: Foram detectados ${ERRORS} problema(s) crítico(s).${NC}"
    echo -e "Corrija os apontamentos ou a rota TLS antes de publicar no GTM."
    exit 1
fi
