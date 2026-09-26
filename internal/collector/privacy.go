package collector

import (
	"net"
	"strings"
)

// ConsentState representa as categorias de consentimento manifestadas pelo usuário
type ConsentState struct {
	Necessary bool `json:"necessary"`
	Analytics bool `json:"analytics"`
	Marketing bool `json:"marketing"`
}

// PrivacySignals representa os sinais técnicos de privacidade emitidos pelo navegador
type PrivacySignals struct {
	GPC bool `json:"gpc"` // Global Privacy Control (Sec-GPC: 1)
	DNT bool `json:"dnt"` // Do Not Track (DNT: 1)
}

// CategoryPolicy define a política de restrição técnica de cada categoria
type CategoryPolicy struct {
	RequiresConsent          bool   `json:"requires_consent"`
	MaskIPMode               string `json:"mask_ip_mode,omitempty"`
	IssueVisitorCookie       bool   `json:"issue_visitor_cookie,omitempty"`
	CookieLifespanDays       int    `json:"cookie_lifespan_days,omitempty"`
	PersistAttributionParams bool   `json:"persist_attribution_params,omitempty"`
	AllowThirdPartyDispatch  bool   `json:"allow_third_party_dispatch,omitempty"`
}

// PrivacySettings modela a governança de privacidade configurada para um site
type PrivacySettings struct {
	Version             string                    `json:"version"`
	EnforceGPC          bool                      `json:"enforce_gpc"`
	MaskIP              bool                      `json:"mask_ip"`
	CategoriesPolicy    map[string]CategoryPolicy `json:"categories_policy,omitempty"`
	RetentionPolicyDays map[string]int           `json:"retention_policy_days,omitempty"`
}

// DefaultPrivacySettings fornece uma configuração segura (Privacy by Default) para sites sem configuração prévia
func DefaultPrivacySettings() *PrivacySettings {
	return &PrivacySettings{
		Version:    "1.0",
		EnforceGPC: true,
		MaskIP:     true,
		CategoriesPolicy: map[string]CategoryPolicy{
			"necessary": {
				RequiresConsent: false,
				MaskIPMode:      "last_octet",
			},
			"analytics": {
				RequiresConsent:    false,
				IssueVisitorCookie: true,
				CookieLifespanDays: 365,
			},
			"marketing": {
				RequiresConsent:          true,
				PersistAttributionParams: true,
				AllowThirdPartyDispatch:  true,
			},
		},
		RetentionPolicyDays: map[string]int{
			"raw_events_clickhouse":        90,
			"aggregated_events_clickhouse": 730,
			"inactive_visitors_postgres":   365,
		},
	}
}

// MaskIP aplica anonimização/mascaramento de IP conforme padrão ANPD/GDPR:
// - IPv4: zera o último octeto (ex: 187.33.241.45 -> 187.33.241.0)
// - IPv6: mascara os últimos 80 bits mantendo o prefixo /48
func MaskIP(rawIP string) string {
	rawIP = strings.TrimSpace(rawIP)
	if rawIP == "" {
		return ""
	}

	ip := net.ParseIP(rawIP)
	if ip == nil {
		return rawIP
	}

	// IPv4
	if ipv4 := ip.To4(); ipv4 != nil {
		mask := net.CIDRMask(24, 32)
		return ipv4.Mask(mask).String()
	}

	// IPv6: máscara /48
	mask := net.CIDRMask(48, 128)
	return ip.Mask(mask).String()
}
