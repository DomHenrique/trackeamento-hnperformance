package prefixedid

import (
	"crypto/rand"
	"math/big"
	"strings"
)

const (
	// Prefixos Institucionais Type-Prefixed IDs
	PrefixSite       = "hn_site_"
	PrefixVisitor    = "hn_vis_"
	PrefixSession    = "hn_ses_"
	PrefixEvent      = "hn_evt_"
	PrefixClient     = "hn_cli_"
	PrefixLegacySite = "hn_live_key_"

	// Alfabeto minúsculo seguro sem caracteres especiais para fácil duplo-clique
	idAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
)

// GenerateRandomString gera uma sequência pseudoaleatória criptograficamente segura
func GenerateRandomString(length int) string {
	if length <= 0 {
		return ""
	}
	result := make([]byte, length)
	alphabetLen := big.NewInt(int64(len(idAlphabet)))
	for i := 0; i < length; i++ {
		num, err := rand.Int(rand.Reader, alphabetLen)
		if err != nil {
			result[i] = idAlphabet[i%len(idAlphabet)]
			continue
		}
		result[i] = idAlphabet[num.Int64()]
	}
	return string(result)
}

// GenerateID combina o prefixo do tipo com uma cadeia alfanumérica aleatória
func GenerateID(prefix string, randomLength int) string {
	return prefix + GenerateRandomString(randomLength)
}

// GenerateSiteKey gera uma chave pública de rastreamento com prefixo hn_site_
func GenerateSiteKey() string {
	return GenerateID(PrefixSite, 20)
}

// GenerateVisitorID gera um identificador de visitante com prefixo hn_vis_
func GenerateVisitorID() string {
	return GenerateID(PrefixVisitor, 24)
}

// GenerateSessionID gera um identificador de sessão com prefixo hn_ses_
func GenerateSessionID() string {
	return GenerateID(PrefixSession, 24)
}

// GenerateEventID gera um identificador de evento para deduplicação com prefixo hn_evt_
func GenerateEventID() string {
	return GenerateID(PrefixEvent, 24)
}

// GenerateClientID gera um identificador de organização/cliente com prefixo hn_cli_
func GenerateClientID() string {
	return GenerateID(PrefixClient, 20)
}

// SanitizeKey remove espaços em branco, quebras de linha e aspas acidentais
func SanitizeKey(key string) string {
	return strings.Trim(strings.TrimSpace(key), "\"'`")
}

// IsValidSiteKey verifica se a chave de site fornecida respeita os padrões da HN Performance
func IsValidSiteKey(key string) bool {
	clean := SanitizeKey(key)
	if clean == "" {
		return false
	}

	// Suporte ao novo padrão hn_site_
	if strings.HasPrefix(clean, PrefixSite) {
		body := clean[len(PrefixSite):]
		if len(body) < 12 || len(body) > 40 {
			return false
		}
		for _, r := range body {
			if !strings.ContainsRune(idAlphabet, r) {
				return false
			}
		}
		return true
	}

	// Retrocompatibilidade com chaves legadas já criadas em produção (ex: hn_live_key_998877665544332211)
	if strings.HasPrefix(clean, PrefixLegacySite) {
		body := clean[len(PrefixLegacySite):]
		return len(body) >= 8 && len(body) <= 48
	}

	return false
}

// ExtractIDType extrai e retorna o prefixo semântico do ID
func ExtractIDType(id string) string {
	clean := SanitizeKey(id)
	for _, prefix := range []string{PrefixSite, PrefixVisitor, PrefixSession, PrefixEvent, PrefixClient, PrefixLegacySite} {
		if strings.HasPrefix(clean, prefix) {
			return prefix
		}
	}
	return "unknown"
}
