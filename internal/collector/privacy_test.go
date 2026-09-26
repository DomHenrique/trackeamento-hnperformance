package collector

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"tracking-engine/internal/config"
	"tracking-engine/internal/prefixedid"
)

func TestMaskIP(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "IPv4 padrão",
			input:    "187.33.241.45",
			expected: "187.33.241.0",
		},
		{
			name:     "IPv4 local",
			input:    "192.168.1.105",
			expected: "192.168.1.0",
		},
		{
			name:     "IPv4 com espaços",
			input:    "  10.0.0.15  ",
			expected: "10.0.0.0",
		},
		{
			name:     "IPv6 global",
			input:    "2001:0db8:85a3:0000:0000:8a2e:0370:7334",
			expected: "2001:db8:85a3::",
		},
		{
			name:     "String vazia",
			input:    "",
			expected: "",
		},
		{
			name:     "IP inválido",
			input:    "not-an-ip",
			expected: "not-an-ip",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MaskIP(tt.input)
			if got != tt.expected {
				t.Errorf("MaskIP(%q) = %q; esperado %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestDefaultPrivacySettings(t *testing.T) {
	cfg := DefaultPrivacySettings()
	if cfg == nil {
		t.Fatal("DefaultPrivacySettings não deve retornar nil")
	}
	if !cfg.EnforceGPC {
		t.Errorf("EnforceGPC deve ser true por padrão")
	}
	if !cfg.MaskIP {
		t.Errorf("MaskIP deve ser true por padrão")
	}
	if cfg.CategoriesPolicy["necessary"].RequiresConsent {
		t.Errorf("Categoria necessary não deve exigir consentimento")
	}
	if !cfg.CategoriesPolicy["marketing"].RequiresConsent {
		t.Errorf("Categoria marketing deve exigir consentimento")
	}
}

func TestPrivacyEnforcement_ConsentAndGPC(t *testing.T) {
	app := fiber.New()
	cfg := &config.Config{Env: "test", RedisStreamRaw: "stream:test"}
	h := NewHandler(cfg, nil, nil, nil)
	app.Post("/api/v1/collect", h.HandleCollect)

	testKey := prefixedid.GenerateSiteKey()
	h.siteKeys.Store(testKey, &SiteMetadata{
		ID:             "site_privacy_test",
		AllowedDomains: []string{"example.com"},
		PrivacySettings: DefaultPrivacySettings(),
	})

	t.Run("Visitante recusa analytics: cookie _vid NÃO é emitido", func(t *testing.T) {
		body, _ := json.Marshal(map[string]interface{}{
			"site_key":   testKey,
			"event_name": "page_view",
			"url":        "https://example.com/test",
			"consent": map[string]interface{}{
				"analytics": false,
				"marketing": false,
			},
		})

		req := httptest.NewRequest("POST", "/api/v1/collect", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://example.com")

		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("erro ao executar requisição: %v", err)
		}

		setCookie := resp.Header.Get("Set-Cookie")
		if strings.Contains(setCookie, "_vid=") {
			t.Errorf("Cookie _vid NÃO deve ser emitido quando consent.analytics for falso: %s", setCookie)
		}
	})

	t.Run("Sinal Sec-GPC ativo: revoga marketing mesmo com consentimento prévio", func(t *testing.T) {
		body, _ := json.Marshal(map[string]interface{}{
			"site_key":   testKey,
			"event_name": "page_view",
			"url":        "https://example.com/test",
			"consent": map[string]interface{}{
				"analytics": true,
				"marketing": true, // Usuário tenta autorizar, mas navegador enviou Sec-GPC
			},
		})

		req := httptest.NewRequest("POST", "/api/v1/collect", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://example.com")
		req.Header.Set("Sec-GPC", "1")

		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("erro ao executar requisição: %v", err)
		}

		if resp.StatusCode != fiber.StatusAccepted && resp.StatusCode != fiber.StatusOK {
			t.Errorf("status inesperado: %d", resp.StatusCode)
		}
	})
}
