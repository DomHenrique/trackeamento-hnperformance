package identity

import "tracking-engine/internal/prefixedid"

const (
	PrefixSite       = prefixedid.PrefixSite
	PrefixVisitor    = prefixedid.PrefixVisitor
	PrefixSession    = prefixedid.PrefixSession
	PrefixEvent      = prefixedid.PrefixEvent
	PrefixClient     = prefixedid.PrefixClient
	PrefixLegacySite = prefixedid.PrefixLegacySite
)

// GenerateRandomString delega para prefixedid.GenerateRandomString
func GenerateRandomString(length int) string {
	return prefixedid.GenerateRandomString(length)
}

// GenerateID delega para prefixedid.GenerateID
func GenerateID(prefix string, randomLength int) string {
	return prefixedid.GenerateID(prefix, randomLength)
}

// GenerateSiteKey gera uma chave pública de rastreamento com prefixo hn_site_
func GenerateSiteKey() string {
	return prefixedid.GenerateSiteKey()
}

// GenerateVisitorID gera um identificador de visitante com prefixo hn_vis_
func GenerateVisitorID() string {
	return prefixedid.GenerateVisitorID()
}

// GenerateSessionID gera um identificador de sessão com prefixo hn_ses_
func GenerateSessionID() string {
	return prefixedid.GenerateSessionID()
}

// GenerateEventID gera um identificador de evento para deduplicação com prefixo hn_evt_
func GenerateEventID() string {
	return prefixedid.GenerateEventID()
}

// GenerateClientID gera um identificador de organização/cliente com prefixo hn_cli_
func GenerateClientID() string {
	return prefixedid.GenerateClientID()
}

// SanitizeKey remove espaços em branco, quebras de linha e aspas acidentais
func SanitizeKey(key string) string {
	return prefixedid.SanitizeKey(key)
}

// IsValidSiteKey verifica se a chave de site fornecida respeita os padrões da HN Performance
func IsValidSiteKey(key string) bool {
	return prefixedid.IsValidSiteKey(key)
}

// ExtractIDType extrai e retorna o prefixo semântico do ID
func ExtractIDType(id string) string {
	return prefixedid.ExtractIDType(id)
}
