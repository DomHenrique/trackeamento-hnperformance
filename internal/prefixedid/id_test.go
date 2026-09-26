package prefixedid

import (
	"strings"
	"testing"
)

func TestIsValidVisitorID(t *testing.T) {
	tests := []struct {
		name  string
		id    string
		valid bool
	}{
		{
			name:  "Empty string",
			id:    "",
			valid: false,
		},
		{
			name:  "Valid hn_vis_ generated ID",
			id:    GenerateVisitorID(),
			valid: true,
		},
		{
			name:  "Valid explicit hn_vis_ with 24 chars",
			id:    "hn_vis_123456789012345678901234",
			valid: true,
		},
		{
			name:  "Invalid hn_vis_ too short (23 chars)",
			id:    "hn_vis_12345678901234567890123",
			valid: false,
		},
		{
			name:  "Invalid hn_vis_ too long (25 chars)",
			id:    "hn_vis_1234567890123456789012345",
			valid: false,
		},
		{
			name:  "Invalid hn_vis_ with uppercase or special characters",
			id:    "hn_vis_12345678901234567890123A",
			valid: false,
		},
		{
			name:  "Invalid hn_vis_ with symbols",
			id:    "hn_vis_12345678901234567890123!",
			valid: false,
		},
		{
			name:  "Valid UUID v4 legacy",
			id:    "c73bcdcc-2669-4bf6-81d3-e4ae73fb11ff",
			valid: true,
		},
		{
			name:  "Valid legacy UUID with v_ prefix",
			id:    "v_c73bcdcc-2669-4bf6-81d3-e4ae73fb11ff",
			valid: true,
		},
		{
			name:  "Arbitrary text / malicious injection",
			id:    "../../../etc/passwd",
			valid: false,
		},
		{
			name:  "SQL injection payload",
			id:    "' OR 1=1 --",
			valid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsValidVisitorID(tt.id)
			if got != tt.valid {
				t.Errorf("IsValidVisitorID(%q) = %v; esperado %v", tt.id, got, tt.valid)
			}
		})
	}
}

func TestGenerateVisitorID(t *testing.T) {
	vid := GenerateVisitorID()
	if !strings.HasPrefix(vid, PrefixVisitor) {
		t.Errorf("GenerateVisitorID() deve iniciar com %s, obtido %s", PrefixVisitor, vid)
	}
	if len(vid) != len(PrefixVisitor)+24 {
		t.Errorf("GenerateVisitorID() tamanho esperado %d, obtido %d", len(PrefixVisitor)+24, len(vid))
	}
	if !IsValidVisitorID(vid) {
		t.Errorf("GenerateVisitorID() gerou ID inválido: %s", vid)
	}
}
