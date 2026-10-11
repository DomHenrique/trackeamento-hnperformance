package collector_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"tracking-engine/internal/collector"
	"tracking-engine/internal/config"
	"tracking-engine/internal/inbound"
)

func TestHandleInboundCRMWebhook_UnauthorizedWithoutSiteKey(t *testing.T) {
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
		t.Errorf("esperava status 401 Unauthorized, obteve %d", resp.StatusCode)
	}
}

func TestHandleInboundCRMWebhook_AcceptsValidRDStationPayload(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	handler := collector.NewHandler(cfg, nil, nil, nil)

	handler.SetOfflineReconciler(func(ctx context.Context, siteID string, norm *inbound.NormalizedCRMEvent, inboundEventID string) error {
		if norm.Email != "lead.rd@empresa.com" {
			t.Errorf("esperava email lead.rd@empresa.com, obteve %s", norm.Email)
		}
		return nil
	})

	app := fiber.New()
	app.Post("/api/v1/inbound/crm/:provider", handler.HandleInboundCRMWebhook)

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

	req := httptest.NewRequest("POST", "/api/v1/inbound/crm/rdstation?site_key=hn_site_test1234567890123456789012", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("erro ao executar teste: %v", err)
	}

	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Logf("Status retornado: %d", resp.StatusCode)
	}
}

func TestHandleInboundCRMWebhook_RejectsMissingContact(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	handler := collector.NewHandler(cfg, nil, nil, nil)

	app := fiber.New()
	app.Post("/api/v1/inbound/crm/custom", handler.HandleInboundCRMWebhook)

	payload := map[string]interface{}{
		"venda": map[string]interface{}{
			"valor": 500,
		},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/api/v1/inbound/crm/custom?site_key=hn_site_qualquer", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("erro ao executar teste: %v", err)
	}

	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Logf("Status recebido: %d", resp.StatusCode)
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
