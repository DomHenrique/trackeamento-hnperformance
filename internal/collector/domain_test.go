package collector

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"tracking-engine/internal/config"
)

func TestCleanHost(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"https://meudominio.com.br", "meudominio.com.br"},
		{"http://meudominio.com.br:8080/caminho?a=1", "meudominio.com.br"},
		{"lp.meudominio.com.br", "lp.meudominio.com.br"},
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
	allowed := []string{"meudominio.com.br", "hnperformancedigital.com.br"}

	tests := []struct {
		host     string
		isDev    bool
		expected bool
	}{
		// Domínios exatos
		{"meudominio.com.br", false, true},
		{"hnperformancedigital.com.br", false, true},

		// Subdomínios permitidos automaticamente
		{"lp.meudominio.com.br", false, true},
		{"checkout.meudominio.com.br", false, true},
		{"app.sub.hnperformancedigital.com.br", false, true},

		// Tentativas fraudulentas (homógrafos / sufixos sem ponto)
		{"fakemeudominio.com.br", false, false},
		{"meudominio.com.br.evil.com", false, false},
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
		name           string
		originHdr      string
		refererHdr     string
		hostHdr        string
		cnameSubdomain string
		reqURL         string
		isServerAuth   bool
		expected       string
	}{
		{
			name:           "Header Origin de navegador legítimo",
			originHdr:      "https://meudominio.com.br",
			refererHdr:     "",
			reqURL:         "https://evil.com/fake",
			isServerAuth:   false,
			cnameSubdomain: "",
			expected:       "meudominio.com.br",
		},
		{
			name:           "Header Referer quando Origin ausente",
			originHdr:      "",
			refererHdr:     "https://lp.meudominio.com.br/contato?utm=1",
			reqURL:         "https://evil.com/fake",
			isServerAuth:   false,
			cnameSubdomain: "",
			expected:       "lp.meudominio.com.br",
		},
		{
			name:           "Rejeita payload sem headers de navegador, sem server auth e fora do CNAME (Abordagem B)",
			originHdr:      "",
			refererHdr:     "",
			reqURL:         "https://meudominio.com.br",
			isServerAuth:   false,
			cnameSubdomain: "track.meudominio.com.br",
			expected:       "",
		},
		{
			name:           "Disparo Server-Side autenticado via X-Server-Key aceita payload",
			originHdr:      "",
			refererHdr:     "",
			reqURL:         "https://meudominio.com.br/agradecimento",
			isServerAuth:   true,
			cnameSubdomain: "",
			expected:       "meudominio.com.br",
		},
		{
			name:           "Tráfego recebido via CNAME First-Party verificado aceita payload",
			originHdr:      "",
			refererHdr:     "",
			hostHdr:        "track.meudominio.com.br",
			reqURL:         "https://meudominio.com.br/produto",
			isServerAuth:   false,
			cnameSubdomain: "track.meudominio.com.br",
			expected:       "meudominio.com.br",
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
				if tt.cnameSubdomain != "" {
					gotDomain = ExtractOriginDomain(c, eventReq, tt.isServerAuth, tt.cnameSubdomain)
				} else {
					gotDomain = ExtractOriginDomain(c, eventReq, tt.isServerAuth)
				}
				return c.SendStatus(fiber.StatusOK)
			})

			httpReq := httptest.NewRequest("POST", "/test", nil)
			if tt.originHdr != "" {
				httpReq.Header.Set("Origin", tt.originHdr)
			}
			if tt.refererHdr != "" {
				httpReq.Header.Set("Referer", tt.refererHdr)
			}
			if tt.hostHdr != "" {
				httpReq.Host = tt.hostHdr
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

func TestHandleCheckCnameAuthorized(t *testing.T) {
	cfg := &config.Config{
		TrackingDomain: "trackeamento.hnperformancedigital.com.br",
	}
	handler := &Handler{
		cfg: cfg,
	}

	app := fiber.New()
	app.Get("/api/v1/internal/validate-domain", handler.HandleCheckCnameAuthorized)

	// Caso 1: Domínio ausente -> 400
	req1 := httptest.NewRequest("GET", "/api/v1/internal/validate-domain", nil)
	resp1, err := app.Test(req1)
	if err != nil {
		t.Fatalf("erro ao executar teste: %v", err)
	}
	if resp1.StatusCode != fiber.StatusBadRequest {
		t.Errorf("esperado 400 para domínio ausente, obteve %d", resp1.StatusCode)
	}
	_ = resp1.Body.Close()

	// Caso 2: Domínio mestre do tracking configurado -> 200 OK
	req2 := httptest.NewRequest("GET", "/api/v1/internal/validate-domain?domain=trackeamento.hnperformancedigital.com.br", nil)
	resp2, err := app.Test(req2)
	if err != nil {
		t.Fatalf("erro ao executar teste: %v", err)
	}
	if resp2.StatusCode != fiber.StatusOK {
		t.Errorf("esperado 200 para tracking domain mestre, obteve %d", resp2.StatusCode)
	}
	_ = resp2.Body.Close()

	// Caso 3: Domínio de terceiro sem banco (fail-closed) -> 503
	req3 := httptest.NewRequest("GET", "/api/v1/internal/validate-domain?domain=desconhecido.com", nil)
	resp3, err := app.Test(req3)
	if err != nil {
		t.Fatalf("erro ao executar teste: %v", err)
	}
	if resp3.StatusCode != fiber.StatusServiceUnavailable {
		t.Errorf("esperado 503 para banco indisponível (fail-closed), obteve %d", resp3.StatusCode)
	}
	_ = resp3.Body.Close()
}

