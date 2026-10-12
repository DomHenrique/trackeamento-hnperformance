package integhandler

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

func TestHandleCreateServerKey_Validation(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	h := NewIntegrationsHandler(cfg, nil, nil)

	app := fiber.New()
	app.Post("/api/v1/sites/:site_id/server-keys", h.HandleCreateServerKey)

	validSiteID := "22222222-2222-2222-2222-222222222222"

	// 1. UUID inválido
	req := httptest.NewRequest("POST", "/api/v1/sites/bad-uuid/server-keys", nil)
	resp, _ := app.Test(req)
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Errorf("esperado 400 para site_id inválido, obtido %d", resp.StatusCode)
	}

	// 2. Body JSON inválido
	reqBadJSON := httptest.NewRequest("POST", "/api/v1/sites/"+validSiteID+"/server-keys", bytes.NewBufferString("{malformed-json"))
	reqBadJSON.Header.Set("Content-Type", "application/json")
	respBadJSON, _ := app.Test(reqBadJSON)
	if respBadJSON.StatusCode != fiber.StatusBadRequest {
		t.Errorf("esperado 400 para JSON malformado, obtido %d", respBadJSON.StatusCode)
	}

	// 3. Nome excedendo 100 caracteres
	longName := strings.Repeat("B", 101)
	bodyLong, _ := json.Marshal(map[string]string{"name": longName})
	reqLong := httptest.NewRequest("POST", "/api/v1/sites/"+validSiteID+"/server-keys", bytes.NewBuffer(bodyLong))
	reqLong.Header.Set("Content-Type", "application/json")
	respLong, _ := app.Test(reqLong)
	if respLong.StatusCode != fiber.StatusBadRequest {
		t.Errorf("esperado 400 para nome > 100 chars, obtido %d", respLong.StatusCode)
	}

	// 4. Sem banco de dados -> 503
	reqDB := httptest.NewRequest("POST", "/api/v1/sites/"+validSiteID+"/server-keys", bytes.NewBufferString(`{"name":"Chave ERP"}`))
	reqDB.Header.Set("Content-Type", "application/json")
	respDB, _ := app.Test(reqDB)
	if respDB.StatusCode != fiber.StatusServiceUnavailable {
		t.Errorf("esperado 503 quando pg for nil, obtido %d", respDB.StatusCode)
	}
}

func TestHandleRevokeServerKey_Validation(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	h := NewIntegrationsHandler(cfg, nil, nil)

	app := fiber.New()
	app.Post("/api/v1/sites/:site_id/server-keys/:key_id/revoke", h.HandleRevokeServerKey)

	// 1. UUIDs inválidos
	req1 := httptest.NewRequest("POST", "/api/v1/sites/bad-uuid/server-keys/22222222-2222-2222-2222-222222222222/revoke", nil)
	resp1, _ := app.Test(req1)
	if resp1.StatusCode != fiber.StatusBadRequest {
		t.Errorf("esperado 400 para site_id inválido, obtido %d", resp1.StatusCode)
	}

	req2 := httptest.NewRequest("POST", "/api/v1/sites/22222222-2222-2222-2222-222222222222/server-keys/bad-key/revoke", nil)
	resp2, _ := app.Test(req2)
	if resp2.StatusCode != fiber.StatusBadRequest {
		t.Errorf("esperado 400 para key_id inválido, obtido %d", resp2.StatusCode)
	}

	// 2. Sem banco de dados -> 503
	req3 := httptest.NewRequest("POST", "/api/v1/sites/22222222-2222-2222-2222-222222222222/server-keys/33333333-3333-3333-3333-333333333333/revoke", nil)
	resp3, _ := app.Test(req3)
	if resp3.StatusCode != fiber.StatusServiceUnavailable {
		t.Errorf("esperado 503 quando pg for nil, obtido %d", resp3.StatusCode)
	}
}

func TestHandleListServerKeys_Validation(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	h := NewIntegrationsHandler(cfg, nil, nil)

	app := fiber.New()
	app.Get("/api/v1/sites/:site_id/server-keys", h.HandleListServerKeys)

	// 1. UUID inválido
	req := httptest.NewRequest("GET", "/api/v1/sites/invalido/server-keys", nil)
	resp, _ := app.Test(req)
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Errorf("esperado 400 para site_id inválido, obtido %d", resp.StatusCode)
	}

	// 2. Sem banco -> 503
	req2 := httptest.NewRequest("GET", "/api/v1/sites/22222222-2222-2222-2222-222222222222/server-keys", nil)
	resp2, _ := app.Test(req2)
	if resp2.StatusCode != fiber.StatusServiceUnavailable {
		t.Errorf("esperado 503 quando pg for nil, obtido %d", resp2.StatusCode)
	}
}

func TestServerKeyHelpers_HashingAndMasking(t *testing.T) {
	rawKey := prefixedid.GenerateServerKey()
	if !strings.HasPrefix(rawKey, "hn_sec_") {
		t.Fatalf("chave gerada deveria ter prefixo hn_sec_, obtido %s", rawKey)
	}

	if !prefixedid.IsValidServerKey(rawKey) {
		t.Fatalf("chave gerada deveria ser considerada válida")
	}

	hash := prefixedid.HashServerKey(rawKey)
	if len(hash) != 64 {
		t.Fatalf("hash SHA-256 deveria ter 64 caracteres hexadecimais, obtido %d", len(hash))
	}

	// Hashing determinístico
	hash2 := prefixedid.HashServerKey(rawKey)
	if hash != hash2 {
		t.Fatalf("hash deveria ser determinístico")
	}

	masked := prefixedid.MaskServerKey(rawKey)
	if !strings.HasPrefix(masked, "hn_sec_") || !strings.HasSuffix(masked, "...") {
		t.Fatalf("chave mascarada deveria seguir o formato hn_sec_... obtido %s", masked)
	}
}
