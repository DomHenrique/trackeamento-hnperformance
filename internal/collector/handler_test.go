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
	"tracking-engine/internal/storage"
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
		if resp.StatusCode != fiber.StatusAccepted {
			t.Errorf("esperado status %d, obtido %d", fiber.StatusAccepted, resp.StatusCode)
		}

		// Valida retorno JSON com status e event_id
		var resBody map[string]interface{}
		_ = json.NewDecoder(resp.Body).Decode(&resBody)
		if resBody["status"] != "accepted" {
			t.Errorf("esperado status 'accepted', obtido %v", resBody["status"])
		}
		if evID, ok := resBody["event_id"].(string); !ok || !strings.HasPrefix(evID, prefixedid.PrefixEvent) {
			t.Errorf("esperado event_id com prefixo %s, obtido %v", prefixedid.PrefixEvent, resBody["event_id"])
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
		if resp.StatusCode != fiber.StatusAccepted {
			t.Errorf("esperado status %d para chave legada, obtido %d", fiber.StatusAccepted, resp.StatusCode)
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
		if resp.StatusCode != fiber.StatusAccepted {
			t.Errorf("esperado status %d para evento de debug, obtido %d", fiber.StatusAccepted, resp.StatusCode)
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
		if resp.StatusCode != fiber.StatusAccepted {
			t.Errorf("esperado status %d para evento com ?hn_debug=true, obtido %d", fiber.StatusAccepted, resp.StatusCode)
		}
	})

	// 7. Rejeição de payload excessivamente grande (> 64KB)
	t.Run("Rejeita payload maior que 64KB com 413", func(t *testing.T) {
		hugeBytes := make([]byte, 65*1024)
		req := httptest.NewRequest("POST", "/api/v1/collect", bytes.NewReader(hugeBytes))
		req.Header.Set("Content-Type", "application/json")

		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("erro ao executar teste: %v", err)
		}
		if resp.StatusCode != fiber.StatusRequestEntityTooLarge {
			t.Errorf("esperado status %d para payload grande, obtido %d", fiber.StatusRequestEntityTooLarge, resp.StatusCode)
		}
	})

	// 8. Falha de serviço quando Redis está indisponível em modo produção
	t.Run("Retorna 503 se Redis falha ao enfileirar em produção", func(t *testing.T) {
		prodCfg := &config.Config{Env: "production", RedisAddr: "127.0.0.1:54321"}
		badRedis, _ := storage.NewRedis(prodCfg)
		prodHandler := &Handler{
			cfg:   prodCfg,
			redis: badRedis,
		}
		testKey := prefixedid.GenerateSiteKey()
		prodHandler.siteKeys.Store(testKey, &SiteMetadata{
			ID:             "site_prod_fail",
			AllowedDomains: []string{"example.com"},
		})

		prodApp := fiber.New()
		prodApp.Post("/api/v1/collect", prodHandler.HandleCollect)

		body, _ := json.Marshal(map[string]interface{}{
			"site_key":   testKey,
			"event_name": "page_view",
			"url":        "https://example.com/home",
		})
		req := httptest.NewRequest("POST", "/api/v1/collect", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://example.com")

		resp, err := prodApp.Test(req)
		if err != nil {
			t.Fatalf("erro ao executar teste: %v", err)
		}
		if resp.StatusCode != fiber.StatusServiceUnavailable {
			t.Errorf("esperado status %d quando Redis falha, obtido %d", fiber.StatusServiceUnavailable, resp.StatusCode)
		}
	})

	// 9. Rejeita visitor_id arbitrário no corpo (prevenção contra Session Fixation / Cookie Poisoning)
	t.Run("Descarta visitor_id arbitrário enviado no payload e gera novo hn_vis_", func(t *testing.T) {
		testKey := prefixedid.GenerateSiteKey()
		h.siteKeys.Store(testKey, &SiteMetadata{
			ID:             "site_sec_test",
			AllowedDomains: []string{"example.com"},
		})

		body, _ := json.Marshal(map[string]interface{}{
			"site_key":   testKey,
			"event_name": "page_view",
			"url":        "https://example.com/home",
			"user_data": map[string]interface{}{
				"visitor_id": "malicious_injected_vid_12345",
			},
		})
		req := httptest.NewRequest("POST", "/api/v1/collect", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://example.com")

		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("erro ao executar teste: %v", err)
		}
		if resp.StatusCode != fiber.StatusAccepted {
			t.Fatalf("esperado status %d, obtido %d", fiber.StatusAccepted, resp.StatusCode)
		}

		setCookie := resp.Header.Get("Set-Cookie")
		if strings.Contains(setCookie, "malicious_injected_vid_12345") {
			t.Errorf("VULNERABILIDADE: o servidor aceitou e gravou no cookie o visitor_id forjado no corpo: %s", setCookie)
		}
		if !strings.Contains(setCookie, "_vid="+prefixedid.PrefixVisitor) {
			t.Errorf("servidor deve emitir novo cookie com prefixo %s, obtido: %s", prefixedid.PrefixVisitor, setCookie)
		}
	})

	// 10. Descarta cookie _vid malformado
	t.Run("Descarta cookie _vid malformado e emite novo hn_vis_", func(t *testing.T) {
		testKey := prefixedid.GenerateSiteKey()
		h.siteKeys.Store(testKey, &SiteMetadata{
			ID:             "site_sec_test_2",
			AllowedDomains: []string{"example.com"},
		})

		body, _ := json.Marshal(map[string]interface{}{
			"site_key":   testKey,
			"event_name": "page_view",
			"url":        "https://example.com/home",
		})
		req := httptest.NewRequest("POST", "/api/v1/collect", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://example.com")
		req.Header.Set("Cookie", "_vid=../../../etc/passwd")

		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("erro ao executar teste: %v", err)
		}
		if resp.StatusCode != fiber.StatusAccepted {
			t.Fatalf("esperado status %d, obtido %d", fiber.StatusAccepted, resp.StatusCode)
		}

		setCookie := resp.Header.Get("Set-Cookie")
		if strings.Contains(setCookie, "../../../etc/passwd") {
			t.Errorf("servidor não deve emitir cookie com valor malformado: %s", setCookie)
		}
		if !strings.Contains(setCookie, "_vid="+prefixedid.PrefixVisitor) {
			t.Errorf("servidor deve emitir novo cookie com prefixo %s, obtido: %s", prefixedid.PrefixVisitor, setCookie)
		}
	})

	// 11. Preserva cookie _vid legítimo
	t.Run("Preserva cookie _vid legítimo", func(t *testing.T) {
		testKey := prefixedid.GenerateSiteKey()
		h.siteKeys.Store(testKey, &SiteMetadata{
			ID:             "site_sec_test_3",
			AllowedDomains: []string{"example.com"},
		})

		validVid := prefixedid.GenerateVisitorID()

		body, _ := json.Marshal(map[string]interface{}{
			"site_key":   testKey,
			"event_name": "page_view",
			"url":        "https://example.com/home",
		})
		req := httptest.NewRequest("POST", "/api/v1/collect", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://example.com")
		req.Header.Set("Cookie", "_vid="+validVid)

		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("erro ao executar teste: %v", err)
		}
		if resp.StatusCode != fiber.StatusAccepted {
			t.Fatalf("esperado status %d, obtido %d", fiber.StatusAccepted, resp.StatusCode)
		}

		setCookie := resp.Header.Get("Set-Cookie")
		if !strings.Contains(setCookie, "_vid="+validVid) {
			t.Errorf("cookie legítimo deve ser preservado. Esperado %s, obtido: %s", validVid, setCookie)
		}
	})
}

func TestResolveClientIP_Security(t *testing.T) {
	app := fiber.New()

	t.Run("Ignora headers forjados quando TRUSTED_PROXIES está vazio (fail-secure)", func(t *testing.T) {
		cfg := &config.Config{
			Env:               "production",
			TrustedProxiesRaw: "",
			TrustedCIDRs:      nil,
		}

		var capturedIP string
		app.Get("/test-ip-empty", func(c *fiber.Ctx) error {
			capturedIP = ResolveClientIP(c, cfg)
			return c.SendStatus(fiber.StatusOK)
		})

		req := httptest.NewRequest("GET", "/test-ip-empty", nil)
		req.Header.Set("X-Real-IP", "198.51.100.1")
		req.Header.Set("CF-Connecting-IP", "203.0.113.199")
		req.Header.Set("X-Forwarded-For", "192.0.2.1")

		_, err := app.Test(req)
		if err != nil {
			t.Fatalf("erro ao executar teste: %v", err)
		}

		if capturedIP == "198.51.100.1" || capturedIP == "203.0.113.199" || capturedIP == "192.0.2.1" {
			t.Errorf("FALHA DE SEGURANÇA: IP forjado foi aceito sem proxy confiável configurado: %s", capturedIP)
		}
		if capturedIP != "0.0.0.0" && capturedIP != "127.0.0.1" {
			t.Logf("IP remoto direto capturado corretamente: %s", capturedIP)
		}
	})

	t.Run("Aceita headers encaminhados quando conexão vem de proxy confiável configurado", func(t *testing.T) {
		// No httptest do Go/Fiber, o RemoteAddr padrão é 0.0.0.0
		cfg := &config.Config{
			Env:               "production",
			TrustedProxiesRaw: "0.0.0.0/32, 127.0.0.1/32",
			TrustedCIDRs:      config.ParseCIDRList("0.0.0.0/32, 127.0.0.1/32"),
		}

		var capturedIP string
		app.Get("/test-ip-trusted", func(c *fiber.Ctx) error {
			capturedIP = ResolveClientIP(c, cfg)
			return c.SendStatus(fiber.StatusOK)
		})

		// 1. Testa CF-Connecting-IP com precedência
		req := httptest.NewRequest("GET", "/test-ip-trusted", nil)
		req.Header.Set("CF-Connecting-IP", "177.18.19.20")
		req.Header.Set("X-Real-IP", "10.0.0.1")
		_, _ = app.Test(req)
		if capturedIP != "177.18.19.20" {
			t.Errorf("esperado CF-Connecting-IP '177.18.19.20', obtido: %s", capturedIP)
		}

		// 2. Testa X-Real-IP como fallback
		req2 := httptest.NewRequest("GET", "/test-ip-trusted", nil)
		req2.Header.Set("X-Real-IP", "189.20.21.22")
		_, _ = app.Test(req2)
		if capturedIP != "189.20.21.22" {
			t.Errorf("esperado X-Real-IP '189.20.21.22', obtido: %s", capturedIP)
		}

		// 3. Testa X-Forwarded-For pegando o primeiro IP legítimo
		req3 := httptest.NewRequest("GET", "/test-ip-trusted", nil)
		req3.Header.Set("X-Forwarded-For", "201.50.60.70, 10.0.0.1")
		_, _ = app.Test(req3)
		if capturedIP != "201.50.60.70" {
			t.Errorf("esperado primeiro IP de X-Forwarded-For '201.50.60.70', obtido: %s", capturedIP)
		}

		// 4. Ignora valores não IP malformados nos headers
		req4 := httptest.NewRequest("GET", "/test-ip-trusted", nil)
		req4.Header.Set("X-Real-IP", "../../../etc/passwd")
		_, _ = app.Test(req4)
		if capturedIP == "../../../etc/passwd" {
			t.Errorf("servidor não deve aceitar string que não seja IP válido: %s", capturedIP)
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

func TestHandleCollect_AutomaticEvents(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	h := &Handler{
		cfg: cfg,
	}

	app := fiber.New()
	app.Post("/api/v1/collect", h.HandleCollect)

	testKey := prefixedid.GenerateSiteKey()
	h.siteKeys.Store(testKey, &SiteMetadata{
		ID:             "site_auto_events",
		AllowedDomains: []string{"spspower.com.br"},
	})

	events := []struct {
		name       string
		eventName  string
		customData map[string]interface{}
	}{
		{
			name:      "Ingestão session_start",
			eventName: "session_start",
			customData: map[string]interface{}{
				"session_number": 1,
			},
		},
		{
			name:      "Ingestão first_visit",
			eventName: "first_visit",
			customData: map[string]interface{}{
				"first_open_time": 1726000000,
			},
		},
		{
			name:      "Ingestão scroll (90% depth)",
			eventName: "scroll",
			customData: map[string]interface{}{
				"percent_scrolled": 90,
			},
		},
		{
			name:      "Ingestão click outbound",
			eventName: "click",
			customData: map[string]interface{}{
				"link_url":    "https://externalpartner.com/contact",
				"link_domain": "externalpartner.com",
				"outbound":    true,
			},
		},
		{
			name:      "Ingestão file_download",
			eventName: "file_download",
			customData: map[string]interface{}{
				"file_name":      "catalogo_sps_2026.pdf",
				"file_extension": "pdf",
			},
		},
	}

	for _, tc := range events {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]interface{}{
				"site_key":    testKey,
				"event_name":  tc.eventName,
				"url":         "https://spspower.com.br/produtos",
				"custom_data": tc.customData,
				"is_debug":    true,
			})
			req := httptest.NewRequest("POST", "/api/v1/collect", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "https://spspower.com.br")

			resp, err := app.Test(req)
			if err != nil {
				t.Fatalf("falha ao enviar evento %s: %v", tc.eventName, err)
			}
			if resp.StatusCode != fiber.StatusAccepted {
				t.Errorf("evento %s: esperado status %d, obtido %d", tc.eventName, fiber.StatusAccepted, resp.StatusCode)
			}
		})
	}
}

