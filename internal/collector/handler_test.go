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

func TestHandleCollect_KeyValidationAndIDs(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	h := &Handler{
		cfg: cfg,
	}

	app := fiber.New()
	app.Post("/api/v1/collect", h.HandleCollect)

	// 1. Falha por site_key ausente
	t.Run("Rejeita site_key ausente", func(t *testing.T) {
		body, _ := json.Marshal(map[string]interface{}{
			"event_name": "page_view",
		})
		req := httptest.NewRequest("POST", "/api/v1/collect", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("erro ao executar teste: %v", err)
		}
		if resp.StatusCode != fiber.StatusUnauthorized {
			t.Errorf("esperado status %d para site_key ausente, obtido %d", fiber.StatusUnauthorized, resp.StatusCode)
		}
	})

	// 2. Falha por site_key inválida / não cadastrada
	t.Run("Rejeita site_key desconhecida", func(t *testing.T) {
		body, _ := json.Marshal(map[string]interface{}{
			"site_key":   "hn_site_unknown1234567890",
			"event_name": "page_view",
		})
		req := httptest.NewRequest("POST", "/api/v1/collect", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("erro ao executar teste: %v", err)
		}
		if resp.StatusCode != fiber.StatusUnauthorized {
			t.Errorf("esperado status %d para site_key desconhecida, obtido %d", fiber.StatusUnauthorized, resp.StatusCode)
		}
	})

	// 3. Sucesso com chave cadastrada no cache (hn_site_...) e verificação de geração de cookie hn_vis_
	t.Run("Aceita hn_site_ cadastrada e gera hn_vis_ e cookie", func(t *testing.T) {
		testKey := prefixedid.GenerateSiteKey()
		h.siteKeys.Store(testKey, &SiteMetadata{
			ID:             "site_123",
			AllowedDomains: []string{"example.com"},
		})

		body, _ := json.Marshal(map[string]interface{}{
			"site_key":   "  " + testKey + "  \n", // Testa sanitização de espaços
			"event_name": "page_view",
			"url":        "https://example.com/home",
		})
		req := httptest.NewRequest("POST", "/api/v1/collect", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://example.com")

		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("erro ao executar teste: %v", err)
		}
		if resp.StatusCode != fiber.StatusNoContent {
			t.Errorf("esperado status %d, obtido %d", fiber.StatusNoContent, resp.StatusCode)
		}

		// Valida que o cookie _vid foi emitido com prefixo hn_vis_
		setCookie := resp.Header.Get("Set-Cookie")
		if !strings.Contains(setCookie, "_vid="+prefixedid.PrefixVisitor) {
			t.Errorf("cookie _vid deve ter prefixo %s, cabeçalho obtido: %s", prefixedid.PrefixVisitor, setCookie)
		}
	})

	// 4. Sucesso com chave legada (hn_live_key_...)
	t.Run("Aceita chave legada hn_live_key_", func(t *testing.T) {
		legacyKey := "hn_live_key_998877665544332211"
		h.siteKeys.Store(legacyKey, &SiteMetadata{
			ID:             "site_legacy",
			AllowedDomains: []string{"legacy.com"},
		})

		body, _ := json.Marshal(map[string]interface{}{
			"site_key":   legacyKey,
			"event_name": "page_view",
			"url":        "https://legacy.com/home",
		})
		req := httptest.NewRequest("POST", "/api/v1/collect", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://legacy.com")

		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("erro ao executar teste: %v", err)
		}
		if resp.StatusCode != fiber.StatusNoContent {
			t.Errorf("esperado status %d para chave legada, obtido %d", fiber.StatusNoContent, resp.StatusCode)
		}
	})
}
