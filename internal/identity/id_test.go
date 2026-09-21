package identity

import (
	"strings"
	"testing"
)

func TestGenerateTypePrefixedIDs(t *testing.T) {
	// 1. Site Key
	siteKey := GenerateSiteKey()
	if !strings.HasPrefix(siteKey, PrefixSite) {
		t.Errorf("GenerateSiteKey() deve começar com %s, obtido %s", PrefixSite, siteKey)
	}
	if len(siteKey) != len(PrefixSite)+20 {
		t.Errorf("tamanho inesperado para GenerateSiteKey: %d", len(siteKey))
	}
	if !IsValidSiteKey(siteKey) {
		t.Errorf("chave recém-gerada deve ser válida: %s", siteKey)
	}

	// 2. Visitor ID
	visID := GenerateVisitorID()
	if !strings.HasPrefix(visID, PrefixVisitor) {
		t.Errorf("GenerateVisitorID() deve começar com %s, obtido %s", PrefixVisitor, visID)
	}
	if len(visID) != len(PrefixVisitor)+24 {
		t.Errorf("tamanho inesperado para GenerateVisitorID: %d", len(visID))
	}

	// 3. Session ID
	sesID := GenerateSessionID()
	if !strings.HasPrefix(sesID, PrefixSession) {
		t.Errorf("GenerateSessionID() deve começar com %s, obtido %s", PrefixSession, sesID)
	}

	// 4. Event ID
	evtID := GenerateEventID()
	if !strings.HasPrefix(evtID, PrefixEvent) {
		t.Errorf("GenerateEventID() deve começar com %s, obtido %s", PrefixEvent, evtID)
	}

	// 5. Client ID
	cliID := GenerateClientID()
	if !strings.HasPrefix(cliID, PrefixClient) {
		t.Errorf("GenerateClientID() deve começar com %s, obtido %s", PrefixClient, cliID)
	}
}

func TestIDUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		key := GenerateSiteKey()
		if seen[key] {
			t.Fatalf("colisão detectada em GenerateSiteKey na iteração %d: %s", i, key)
		}
		seen[key] = true
	}
}

func TestSanitizeKey(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"  hn_site_1234567890abcdef  ", "hn_site_1234567890abcdef"},
		{"\n\thn_site_1234567890abcdef\r\n", "hn_site_1234567890abcdef"},
		{"\"hn_site_1234567890abcdef\"", "hn_site_1234567890abcdef"},
		{"'hn_site_1234567890abcdef'", "hn_site_1234567890abcdef"},
		{"`hn_site_1234567890abcdef`", "hn_site_1234567890abcdef"},
	}

	for _, tt := range tests {
		got := SanitizeKey(tt.input)
		if got != tt.expected {
			t.Errorf("SanitizeKey(%q) = %q; esperado %q", tt.input, got, tt.expected)
		}
	}
}

func TestIsValidSiteKey(t *testing.T) {
	tests := []struct {
		key      string
		expected bool
	}{
		{"hn_site_9k8m2x7v1b3n4p6q8r0s", true},
		{"  hn_site_9k8m2x7v1b3n4p6q8r0s  ", true},
		{"hn_live_key_998877665544332211", true}, // Retrocompatibilidade legado
		{"hn_vis_9k8m2x7v1b3n4p6q8r0s", false},   // Prefixo de visitante usado como site_key
		{"hn_evt_9k8m2x7v1b3n4p6q8r0s", false},   // Prefixo de evento usado como site_key
		{"hn_site_UPPERCASE123456", false},       // Letras maiúsculas
		{"hn_site_short", false},                  // Curto demais
		{"", false},
		{"random_string_123", false},
	}

	for _, tt := range tests {
		got := IsValidSiteKey(tt.key)
		if got != tt.expected {
			t.Errorf("IsValidSiteKey(%q) = %v; esperado %v", tt.key, got, tt.expected)
		}
	}
}

func TestExtractIDType(t *testing.T) {
	if ExtractIDType("hn_site_123") != PrefixSite {
		t.Errorf("esperado %s", PrefixSite)
	}
	if ExtractIDType("hn_vis_123") != PrefixVisitor {
		t.Errorf("esperado %s", PrefixVisitor)
	}
	if ExtractIDType("hn_ses_123") != PrefixSession {
		t.Errorf("esperado %s", PrefixSession)
	}
	if ExtractIDType("hn_evt_123") != PrefixEvent {
		t.Errorf("esperado %s", PrefixEvent)
	}
	if ExtractIDType("random_id") != "unknown" {
		t.Errorf("esperado unknown")
	}
}
