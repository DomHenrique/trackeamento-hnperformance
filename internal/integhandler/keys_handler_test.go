package integhandler

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"tracking-engine/internal/config"
)

func TestHandleCreateSiteKey_Validation(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	h := NewIntegrationsHandler(cfg, nil, nil)

	app := fiber.New()
	app.Post("/api/v1/sites/:site_id/keys", h.HandleCreateSiteKey)

	validSiteID := "11111111-1111-1111-1111-111111111111"

	// 1. UUID inválido
	req := httptest.NewRequest("POST", "/api/v1/sites/invalid-uuid/keys", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("erro ao executar request: %v", err)
	}
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Errorf("esperado 400 para site_id inválido, obtido %d", resp.StatusCode)
	}

	// 2. Body JSON inválido
	reqBadJSON := httptest.NewRequest("POST", "/api/v1/sites/"+validSiteID+"/keys", bytes.NewBufferString("{invalid-json"))
	reqBadJSON.Header.Set("Content-Type", "application/json")
	respBadJSON, err := app.Test(reqBadJSON)
	if err != nil {
		t.Fatalf("erro ao executar request: %v", err)
	}
	if respBadJSON.StatusCode != fiber.StatusBadRequest {
		t.Errorf("esperado 400 para JSON inválido, obtido %d", respBadJSON.StatusCode)
	}

	// 3. Nome excedendo 100 caracteres
	longName := strings.Repeat("A", 101)
	bodyLong, _ := json.Marshal(map[string]string{"name": longName})
	reqLong := httptest.NewRequest("POST", "/api/v1/sites/"+validSiteID+"/keys", bytes.NewBuffer(bodyLong))
	reqLong.Header.Set("Content-Type", "application/json")
	respLong, err := app.Test(reqLong)
	if err != nil {
		t.Fatalf("erro ao executar request: %v", err)
	}
	if respLong.StatusCode != fiber.StatusBadRequest {
		t.Errorf("esperado 400 para nome > 100 chars, obtido %d", respLong.StatusCode)
	}

	// 4. Sem banco de dados -> 503
	reqDB := httptest.NewRequest("POST", "/api/v1/sites/"+validSiteID+"/keys", bytes.NewBufferString(`{"name":"Chave Valida"}`))
	reqDB.Header.Set("Content-Type", "application/json")
	respDB, err := app.Test(reqDB)
	if err != nil {
		t.Fatalf("erro ao executar request: %v", err)
	}
	if respDB.StatusCode != fiber.StatusServiceUnavailable {
		t.Errorf("esperado 503 quando pg for nil, obtido %d", respDB.StatusCode)
	}
}

func TestHandleRevokeSiteKey_Validation(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	h := NewIntegrationsHandler(cfg, nil, nil)

	app := fiber.New()
	app.Post("/api/v1/sites/:site_id/keys/:key_id/revoke", h.HandleRevokeSiteKey)

	// 1. UUIDs inválidos
	req := httptest.NewRequest("POST", "/api/v1/sites/bad-uuid/keys/11111111-1111-1111-1111-111111111111/revoke", nil)
	resp, _ := app.Test(req)
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Errorf("esperado 400 para site_id inválido, obtido %d", resp.StatusCode)
	}

	req2 := httptest.NewRequest("POST", "/api/v1/sites/11111111-1111-1111-1111-111111111111/keys/bad-uuid/revoke", nil)
	resp2, _ := app.Test(req2)
	if resp2.StatusCode != fiber.StatusBadRequest {
		t.Errorf("esperado 400 para key_id inválido, obtido %d", resp2.StatusCode)
	}

	// 2. Sem banco de dados -> 503
	req3 := httptest.NewRequest("POST", "/api/v1/sites/11111111-1111-1111-1111-111111111111/keys/22222222-2222-2222-2222-222222222222/revoke", nil)
	resp3, _ := app.Test(req3)
	if resp3.StatusCode != fiber.StatusServiceUnavailable {
		t.Errorf("esperado 503 quando pg for nil, obtido %d", resp3.StatusCode)
	}
}

func TestHandleListSiteKeys_Validation(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	h := NewIntegrationsHandler(cfg, nil, nil)

	app := fiber.New()
	app.Get("/api/v1/sites/:site_id/keys", h.HandleListSiteKeys)

	// 1. UUID inválido
	req := httptest.NewRequest("GET", "/api/v1/sites/invalid/keys", nil)
	resp, _ := app.Test(req)
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Errorf("esperado 400 para site_id inválido, obtido %d", resp.StatusCode)
	}

	// 2. Sem banco -> 503
	req2 := httptest.NewRequest("GET", "/api/v1/sites/11111111-1111-1111-1111-111111111111/keys", nil)
	resp2, _ := app.Test(req2)
	if resp2.StatusCode != fiber.StatusServiceUnavailable {
		t.Errorf("esperado 503 quando pg for nil, obtido %d", resp2.StatusCode)
	}
}
