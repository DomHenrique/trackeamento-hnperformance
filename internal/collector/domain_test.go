package collector

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestCleanHost(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"https://spspower.com.br", "spspower.com.br"},
		{"http://spspower.com.br:8080/caminho?a=1", "spspower.com.br"},
		{"lp.spspower.com.br", "lp.spspower.com.br"},
		{"https://sub.dominio.com.br:443", "sub.dominio.com.br"},
		{"", ""},
	}

	for _, tt := range tests {
		got := cleanHost(tt.input)
		if got != tt.expected {
			t.Errorf("cleanHost(%q) = %q, esperado %q", tt.input, got, tt.expected)
		}
	}
}

func TestIsDomainAllowed(t *testing.T) {
	allowed := []string{"spspower.com.br", "hnperformancedigital.com.br"}

	tests := []struct {
		host     string
		isDev    bool
		expected bool
	}{
		// Domínios exatos
		{"spspower.com.br", false, true},
		{"hnperformancedigital.com.br", false, true},

		// Subdomínios permitidos automaticamente
		{"lp.spspower.com.br", false, true},
		{"checkout.spspower.com.br", false, true},
		{"app.sub.hnperformancedigital.com.br", false, true},

		// Tentativas fraudulentas (homógrafos / sufixos sem ponto)
		{"fakespspower.com.br", false, false},
		{"spspower.com.br.evil.com", false, false},
		{"outrosite.com.br", false, false},
		{"", false, false},

		// Desenvolvimento (localhost / 127.0.0.1)
		{"localhost", true, true},
		{"127.0.0.1", true, true},
		{"localhost", false, false},
	}

	for _, tt := range tests {
		got := IsDomainAllowed(tt.host, allowed, tt.isDev)
		if got != tt.expected {
			t.Errorf("IsDomainAllowed(%q, allowed, %v) = %v, esperado %v", tt.host, tt.isDev, got, tt.expected)
		}
	}
}

func TestExtractOriginDomain(t *testing.T) {
	tests := []struct {
		name         string
		originHdr    string
		refererHdr   string
		reqURL       string
		isServerAuth bool
		expected     string
	}{
		{
			name:         "Header Origin de navegador legítimo",
			originHdr:    "https://spspower.com.br",
			refererHdr:   "",
			reqURL:       "https://evil.com/fake",
			isServerAuth: false,
			expected:     "spspower.com.br",
		},
		{
			name:         "Header Referer quando Origin ausente",
			originHdr:    "",
			refererHdr:   "https://lp.spspower.com.br/contato?utm=1",
			reqURL:       "https://evil.com/fake",
			isServerAuth: false,
			expected:     "lp.spspower.com.br",
		},
		{
			name:         "Tentativa de spoofing via payload req.URL sem headers de navegador",
			originHdr:    "",
			refererHdr:   "",
			reqURL:       "https://spspower.com.br",
			isServerAuth: false,
			expected:     "", // Bloqueado! Não confia em payload se não autenticado
		},
		{
			name:         "Disparo Server-Side autenticado via X-Server-Key aceita payload",
			originHdr:    "",
			refererHdr:   "",
			reqURL:       "https://spspower.com.br/agradecimento",
			isServerAuth: true,
			expected:     "spspower.com.br",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := fiber.New()
			var gotDomain string

			app.Post("/test", func(c *fiber.Ctx) error {
				eventReq := &EventRequest{
					URL: tt.reqURL,
				}
				gotDomain = ExtractOriginDomain(c, eventReq, tt.isServerAuth)
				return c.SendStatus(fiber.StatusOK)
			})

			httpReq := httptest.NewRequest("POST", "/test", nil)
			if tt.originHdr != "" {
				httpReq.Header.Set("Origin", tt.originHdr)
			}
			if tt.refererHdr != "" {
				httpReq.Header.Set("Referer", tt.refererHdr)
			}

			resp, err := app.Test(httpReq)
			if err != nil {
				t.Fatalf("erro ao executar app.Test: %v", err)
			}
			_ = resp.Body.Close()

			if gotDomain != tt.expected {
				t.Errorf("ExtractOriginDomain() = %q, esperado %q", gotDomain, tt.expected)
			}
		})
	}
}
