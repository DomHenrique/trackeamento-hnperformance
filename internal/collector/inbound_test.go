package collector_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"tracking-engine/internal/collector"
	"tracking-engine/internal/config"
)

func TestHandleInboundCRMWebhook_UnauthorizedWithoutServerKey(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	handler := collector.NewHandler(cfg, nil, nil, nil)

	app := fiber.New()
	app.Post("/api/v1/inbound/crm/:provider", handler.HandleInboundCRMWebhook)

	req := httptest.NewRequest("POST", "/api/v1/inbound/crm/rdstation", bytes.NewReader([]byte(`{"email":"teste@exemplo.com"}`)))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("erro ao executar teste: %v", err)
	}

	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Errorf("esperava status 401 Unauthorized para chamada sem credencial, obteve %d", resp.StatusCode)
	}
}

func TestHandleInboundCRMWebhook_RejectsPublicKeyWith403(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	handler := collector.NewHandler(cfg, nil, nil, nil)

	app := fiber.New()
	app.Post("/api/v1/inbound/crm/:provider", handler.HandleInboundCRMWebhook)

	body := []byte(`{"email":"lead@empresa.com"}`)

	// 1. Rejeição com ?site_key=hn_site_...
	req1 := httptest.NewRequest("POST", "/api/v1/inbound/crm/rdstation?site_key=hn_site_test1234567890", bytes.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	resp1, err := app.Test(req1)
	if err != nil {
		t.Fatalf("erro ao executar teste: %v", err)
	}
	if resp1.StatusCode != fiber.StatusForbidden {
		t.Errorf("esperava status 403 Forbidden para tentativa com site_key na query, obteve %d", resp1.StatusCode)
	}

	// 2. Rejeição com header X-Site-Key
	req2 := httptest.NewRequest("POST", "/api/v1/inbound/crm/rdstation", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Site-Key", "hn_site_abc123456789")
	resp2, err := app.Test(req2)
	if err != nil {
		t.Fatalf("erro ao executar teste: %v", err)
	}
	if resp2.StatusCode != fiber.StatusForbidden {
		t.Errorf("esperava status 403 Forbidden para tentativa com X-Site-Key, obteve %d", resp2.StatusCode)
	}
}

func TestHandleInboundCRMWebhook_AcceptsServerKey(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	handler := collector.NewHandler(cfg, nil, nil, nil)

	meta := &collector.ServerKeyMetadata{
		ID:        "server-key-uuid-1",
		SiteID:    "site-uuid-123",
		Name:      "Chave de Teste ERP",
		Scopes:    []string{"crm:inbound", "erp:conversions"},
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}

	persisted := false
	handler.SetInboundPersister(func(ctx context.Context, siteID, provider, eventName, status, email, phone string, val float64, valCents int64, cur, orderID, dedupKey string, occurredAt time.Time, rawPayload map[string]interface{}) (*collector.InboundPersistResult, error) {
		persisted = true
		if valCents != 250000 {
			t.Errorf("esperava value_cents 250000, obteve %d", valCents)
		}
		if status != "pending" {
			t.Errorf("esperava status pending, obteve %s", status)
		}
		return &collector.InboundPersistResult{
			InboundID:   "inbound-uuid-test-999",
			IsDuplicate: false,
		}, nil
	})

	app := fiber.New()
	app.Post("/api/v1/inbound/crm/:provider", func(c *fiber.Ctx) error {
		c.Locals("server_auth", meta)
		return handler.HandleInboundCRMWebhook(c)
	})

	payload := map[string]interface{}{
		"leads": []map[string]interface{}{
			{
				"id":           "lead_12345",
				"email":        "lead.rd@empresa.com",
				"mobile_phone": "11988887777",
				"opportunity":  "true",
				"value":        2500.00,
			},
		},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/api/v1/inbound/crm/rdstation", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("erro ao executar teste: %v", err)
	}

	if resp.StatusCode != fiber.StatusAccepted {
		t.Errorf("esperava status 202 Accepted para chave de servidor autorizada, obteve %d", resp.StatusCode)
	}

	if !persisted {
		t.Errorf("esperava que a persistência fosse executada")
	}
}

func TestHandleInboundCRMWebhook_IdempotentDuplicateReturns200(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	handler := collector.NewHandler(cfg, nil, nil, nil)

	meta := &collector.ServerKeyMetadata{
		ID:        "server-key-uuid-1",
		SiteID:    "site-uuid-123",
		Scopes:    []string{"crm:inbound"},
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}

	// Simula detecção de evento duplicado já persistido
	handler.SetInboundPersister(func(ctx context.Context, siteID, provider, eventName, status, email, phone string, val float64, valCents int64, cur, orderID, dedupKey string, occurredAt time.Time, rawPayload map[string]interface{}) (*collector.InboundPersistResult, error) {
		return &collector.InboundPersistResult{
			InboundID:   "inbound-existing-prev-id",
			IsDuplicate: true,
		}, nil
	})

	app := fiber.New()
	app.Post("/api/v1/inbound/crm/:provider", func(c *fiber.Ctx) error {
		c.Locals("server_auth", meta)
		return handler.HandleInboundCRMWebhook(c)
	})

	payload := map[string]interface{}{
		"leads": []map[string]interface{}{
			{
				"id":          "lead_99",
				"email":       "duplicado@empresa.com",
				"opportunity": "true",
			},
		},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/api/v1/inbound/crm/rdstation", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("erro ao executar teste: %v", err)
	}

	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("esperava status 200 OK para evento duplicado, obteve %d", resp.StatusCode)
	}

	var resBody map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&resBody)
	if resBody["status"] != "duplicate_ignored" {
		t.Errorf("esperava status duplicate_ignored, obteve %v", resBody["status"])
	}
	if resBody["inbound_id"] != "inbound-existing-prev-id" {
		t.Errorf("esperava inbound_id existente, obteve %v", resBody["inbound_id"])
	}
}

func TestHandleInboundCRMWebhook_DatabaseFailureReturns500(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	handler := collector.NewHandler(cfg, nil, nil, nil)

	meta := &collector.ServerKeyMetadata{
		ID:        "server-key-uuid-1",
		SiteID:    "site-uuid-123",
		Scopes:    []string{"crm:inbound"},
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}

	// Simula erro de escrita no Postgres
	handler.SetInboundPersister(func(ctx context.Context, siteID, provider, eventName, status, email, phone string, val float64, valCents int64, cur, orderID, dedupKey string, occurredAt time.Time, rawPayload map[string]interface{}) (*collector.InboundPersistResult, error) {
		return nil, errors.New("timeout de conexão no postgres")
	})

	app := fiber.New()
	app.Post("/api/v1/inbound/crm/:provider", func(c *fiber.Ctx) error {
		c.Locals("server_auth", meta)
		return handler.HandleInboundCRMWebhook(c)
	})

	body := []byte(`{"leads":[{"email":"falha@teste.com","opportunity":"true"}]}`)
	req := httptest.NewRequest("POST", "/api/v1/inbound/crm/rdstation", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("erro ao executar teste: %v", err)
	}

	if resp.StatusCode != fiber.StatusInternalServerError {
		t.Errorf("esperava status 500 para falha de banco (fail-fast), obteve %d", resp.StatusCode)
	}
}

func TestHandleInboundCRMWebhook_DatabaseUnavailableReturns503(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	// Sem persister e sem pool Postgres
	handler := collector.NewHandler(cfg, nil, nil, nil)

	meta := &collector.ServerKeyMetadata{
		ID:        "server-key-uuid-1",
		SiteID:    "site-uuid-123",
		Scopes:    []string{"crm:inbound"},
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}

	app := fiber.New()
	app.Post("/api/v1/inbound/crm/:provider", func(c *fiber.Ctx) error {
		c.Locals("server_auth", meta)
		return handler.HandleInboundCRMWebhook(c)
	})

	body := []byte(`{"leads":[{"email":"teste@indisponivel.com","opportunity":"true"}]}`)
	req := httptest.NewRequest("POST", "/api/v1/inbound/crm/rdstation", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("erro ao executar teste: %v", err)
	}

	if resp.StatusCode != fiber.StatusServiceUnavailable {
		t.Errorf("esperava status 503 quando pool Postgres estiver ausente, obteve %d", resp.StatusCode)
	}
}

func TestHandleInboundCRMWebhook_PayloadLimit256KB(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	handler := collector.NewHandler(cfg, nil, nil, nil)

	app := fiber.New()
	app.Post("/api/v1/inbound/crm/:provider", handler.HandleInboundCRMWebhook)

	// Cria payload maior que 256 KB
	hugeBody := make([]byte, 260*1024)
	req := httptest.NewRequest("POST", "/api/v1/inbound/crm/rdstation", bytes.NewReader(hugeBody))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("erro ao executar teste: %v", err)
	}

	if resp.StatusCode != fiber.StatusRequestEntityTooLarge {
		t.Errorf("esperava status 413 Payload Too Large para corpo > 256KB, obteve %d", resp.StatusCode)
	}
}

func TestRequireServerKeyAuth_MiddlewareBehavior(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	handler := collector.NewHandler(cfg, nil, nil, nil)

	app := fiber.New()
	// Rota protegida exigindo escopo crm:inbound
	app.Post("/api/v1/sites/:site_id/inbound/events", handler.RequireServerKeyAuth("crm:inbound"), func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusOK).JSON(fiber.Map{"status": "ok"})
	})

	// 1. Sem chave -> 401
	req1 := httptest.NewRequest("POST", "/api/v1/sites/site-123/inbound/events", bytes.NewReader([]byte("{}")))
	resp1, _ := app.Test(req1)
	if resp1.StatusCode != fiber.StatusUnauthorized {
		t.Errorf("esperava 401 para requisição sem chave, obteve %d", resp1.StatusCode)
	}

	// 2. Chave de site (pública) -> 403 Forbidden
	req2 := httptest.NewRequest("POST", "/api/v1/sites/site-123/inbound/events", bytes.NewReader([]byte("{}")))
	req2.Header.Set("X-Site-Key", "hn_site_tentativa_publica")
	resp2, _ := app.Test(req2)
	if resp2.StatusCode != fiber.StatusForbidden {
		t.Errorf("esperava 403 Forbidden ao tentar usar chave client-side, obteve %d", resp2.StatusCode)
	}
}

func TestHandleListInboundEvents_InvalidSiteID(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	handler := collector.NewHandler(cfg, nil, nil, nil)

	app := fiber.New()
	app.Get("/api/v1/sites/:site_id/inbound-events", handler.HandleListInboundEvents)

	req := httptest.NewRequest("GET", "/api/v1/sites/id-invalido/inbound-events", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("erro ao executar teste: %v", err)
	}

	if resp.StatusCode != fiber.StatusBadRequest {
		t.Errorf("esperava status 400 Bad Request para UUID inválido, obteve %d", resp.StatusCode)
	}
}
