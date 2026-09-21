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

	// 5. Sucesso em modo debug (is_debug: true no corpo)
	t.Run("Aceita evento com flag is_debug no corpo", func(t *testing.T) {
		testKey := prefixedid.GenerateSiteKey()
		h.siteKeys.Store(testKey, &SiteMetadata{
			ID:             "site_debug_test",
			AllowedDomains: []string{"example.com"},
		})

		body, _ := json.Marshal(map[string]interface{}{
			"site_key":   testKey,
			"event_name": "lead",
			"url":        "https://example.com/lead",
			"is_debug":   true,
			"user_data": map[string]interface{}{
				"email": "debug.user@example.com",
			},
		})
		req := httptest.NewRequest("POST", "/api/v1/collect", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://example.com")

		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("erro ao executar teste: %v", err)
		}
		if resp.StatusCode != fiber.StatusNoContent {
			t.Errorf("esperado status %d para evento de debug, obtido %d", fiber.StatusNoContent, resp.StatusCode)
		}
	})

	// 6. Sucesso em modo debug via query param (?hn_debug=true)
	t.Run("Aceita evento com flag de debug na URL (?hn_debug=true)", func(t *testing.T) {
		testKey := prefixedid.GenerateSiteKey()
		h.siteKeys.Store(testKey, &SiteMetadata{
			ID:             "site_debug_query",
			AllowedDomains: []string{"example.com"},
		})

		body, _ := json.Marshal(map[string]interface{}{
			"site_key":   testKey,
			"event_name": "page_view",
			"url":        "https://example.com/home",
		})
		req := httptest.NewRequest("POST", "/api/v1/collect?hn_debug=true", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://example.com")

		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("erro ao executar teste: %v", err)
		}
		if resp.StatusCode != fiber.StatusNoContent {
			t.Errorf("esperado status %d para evento com ?hn_debug=true, obtido %d", fiber.StatusNoContent, resp.StatusCode)
		}
	})
}

func TestHandleDebugSimulateAndClear(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	h := &Handler{
		cfg: cfg,
	}

	app := fiber.New()
	app.Post("/api/v1/debug/simulate", h.HandleDebugSimulate)
	app.Post("/api/v1/debug/clear", h.HandleDebugClear)

	t.Run("Rejeita simulação sem site_id", func(t *testing.T) {
		body, _ := json.Marshal(map[string]interface{}{
			"event_name": "lead",
		})
		req := httptest.NewRequest("POST", "/api/v1/debug/simulate", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("erro ao executar teste: %v", err)
		}
		if resp.StatusCode != fiber.StatusBadRequest {
			t.Errorf("esperado status %d para simulação sem site_id, obtido %d", fiber.StatusBadRequest, resp.StatusCode)
		}
	})

	t.Run("Simula evento com sucesso e gera identificadores prefixados", func(t *testing.T) {
		body, _ := json.Marshal(map[string]interface{}{
			"site_id":    "site_sim_123",
			"event_name": "purchase",
			"page_url":   "https://loja.exemplo.com/checkout?utm_source=teste",
			"user_data": map[string]interface{}{
				"email": "comprador@exemplo.com",
			},
			"custom_data": map[string]interface{}{
				"value":    299.90,
				"currency": "BRL",
			},
		})
		req := httptest.NewRequest("POST", "/api/v1/debug/simulate", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("erro ao executar teste: %v", err)
		}
		if resp.StatusCode != fiber.StatusOK {
			t.Fatalf("esperado status %d para simulação válida, obtido %d", fiber.StatusOK, resp.StatusCode)
		}

		var result struct {
			Status  string       `json:"status"`
			EventID string       `json:"event_id"`
			Payload EventPayload `json:"payload"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("erro ao deserializar resposta da simulação: %v", err)
		}

		if result.Status != "success" {
			t.Errorf("esperado status 'success', obtido '%s'", result.Status)
		}
		if !strings.HasPrefix(result.EventID, prefixedid.PrefixEvent) {
			t.Errorf("event_id simulado deve ter prefixo %s, obtido %s", prefixedid.PrefixEvent, result.EventID)
		}
		if !result.Payload.IsDebug {
			t.Errorf("payload de simulação deve ter IsDebug = true")
		}
		if result.Payload.Attribution.UTMSource != "teste" {
			t.Errorf("esperado utm_source 'teste', obtido '%s'", result.Payload.Attribution.UTMSource)
		}
	})

	t.Run("Limpa buffer de debug com sucesso", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v1/debug/clear?site_id=site_sim_123", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("erro ao executar teste: %v", err)
		}
		if resp.StatusCode != fiber.StatusOK {
			t.Errorf("esperado status %d para clear com site_id, obtido %d", fiber.StatusOK, resp.StatusCode)
		}
	})

	t.Run("Rejeita limpeza de debug sem site_id", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v1/debug/clear", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("erro ao executar teste: %v", err)
		}
		if resp.StatusCode != fiber.StatusBadRequest {
			t.Errorf("esperado status %d para clear sem site_id, obtido %d", fiber.StatusBadRequest, resp.StatusCode)
		}
	})
}
