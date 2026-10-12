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

func TestMaskToken(t *testing.T) {
	short := "12345"
	if masked := maskToken(short); masked != "••••••••" {
		t.Errorf("esperado '••••••••', obtido '%s'", masked)
	}

	fifteenChars := "123456789012345"
	if masked := maskToken(fifteenChars); masked != "••••••••" {
		t.Errorf("esperado '••••••••' para 15 chars, obtido '%s'", masked)
	}

	longToken := "EAAGm0PX9123456789abcdef" // 24 chars
	masked := maskToken(longToken)
	if masked != "••••••••cdef" {
		t.Errorf("esperado '••••••••cdef', obtido '%s'", masked)
	}
}

func TestMaskSensitiveQueryParams(t *testing.T) {
	raw := "https://hooks.example.com/webhook?token=EAAGm0PX9123456789abcdef&client_id=123"
	masked := maskSensitiveQueryParams(raw)
	if !strings.Contains(masked, "••••••••cdef") && !strings.Contains(masked, "%E2%80%A2%E2%80%A2%E2%80%A2%E2%80%A2%E2%80%A2%E2%80%A2%E2%80%A2%E2%80%A2cdef") {
		t.Errorf("esperava token mascarado na url, obtido: %s", masked)
	}
	if !strings.Contains(masked, "client_id=123") {
		t.Errorf("esperava parametro nao sensivel preservado, obtido: %s", masked)
	}
}

func TestHandleGetIntegrations_InvalidUUID(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	h := NewIntegrationsHandler(cfg, nil, nil)

	app := fiber.New()
	app.Get("/api/v1/sites/:site_id/integrations", h.HandleGetIntegrations)

	req := httptest.NewRequest("GET", "/api/v1/sites/invalid-uuid-123/integrations", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("erro na requisicao: %v", err)
	}

	if resp.StatusCode != fiber.StatusBadRequest {
		t.Errorf("esperado %d, obtido %d", fiber.StatusBadRequest, resp.StatusCode)
	}
}

func TestHandleSaveIntegration_UnsupportedPlatform(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	h := NewIntegrationsHandler(cfg, nil, nil)

	app := fiber.New()
	app.Put("/api/v1/sites/:site_id/integrations/:platform", h.HandleSaveIntegration)

	validUUID := "22222222-2222-2222-2222-222222222222"
	reqBody := `{"is_active": true, "credentials": {}}`
	req := httptest.NewRequest("PUT", "/api/v1/sites/"+validUUID+"/integrations/invalid_platform", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("erro na requisicao: %v", err)
	}

	if resp.StatusCode != fiber.StatusBadRequest {
		t.Errorf("esperado %d, obtido %d", fiber.StatusBadRequest, resp.StatusCode)
	}
}

func TestHandleSaveIntegration_SSRFValidation(t *testing.T) {
	cfg := &config.Config{Env: "production"}
	h := NewIntegrationsHandler(cfg, nil, nil)

	app := fiber.New()
	app.Put("/api/v1/sites/:site_id/integrations/:platform", h.HandleSaveIntegration)

	validUUID := "22222222-2222-2222-2222-222222222222"

	// 1. Webhook com IP restrito em produção
	reqBody := `{"is_active": true, "credentials": {"url": "http://169.254.169.254/latest/meta-data"}}`
	req := httptest.NewRequest("PUT", "/api/v1/sites/"+validUUID+"/integrations/webhook", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Errorf("esperava 400 ao tentar salvar webhook com IP restrito, obteve %d", resp.StatusCode)
	}

	// 2. Google Ads com IP restrito em produção
	reqBodyGads := `{"is_active": true, "credentials": {"endpoint_url": "http://127.0.0.1:8080/upload"}}`
	reqGads := httptest.NewRequest("PUT", "/api/v1/sites/"+validUUID+"/integrations/google_ads", bytes.NewBufferString(reqBodyGads))
	reqGads.Header.Set("Content-Type", "application/json")
	respGads, _ := app.Test(reqGads)
	if respGads.StatusCode != fiber.StatusBadRequest {
		t.Errorf("esperava 400 ao tentar salvar google_ads com IP restrito, obteve %d", respGads.StatusCode)
	}
}

func TestHandleTestIntegration_MissingCreds(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	h := NewIntegrationsHandler(cfg, nil, nil)

	app := fiber.New()
	app.Post("/api/v1/sites/:site_id/integrations/:platform/test", h.HandleTestIntegration)

	validUUID := "22222222-2222-2222-2222-222222222222"

	// 1. Meta CAPI sem token
	reqMeta := httptest.NewRequest("POST", "/api/v1/sites/"+validUUID+"/integrations/meta_capi/test", bytes.NewBufferString(`{"credentials": {"pixel_id": "123"}}`))
	reqMeta.Header.Set("Content-Type", "application/json")
	respMeta, _ := app.Test(reqMeta)
	if respMeta.StatusCode != fiber.StatusBadRequest {
		t.Errorf("Meta sem token deveria retornar 400, obteve %d", respMeta.StatusCode)
	}

	// 2. GA4 sem api_secret
	reqGA4 := httptest.NewRequest("POST", "/api/v1/sites/"+validUUID+"/integrations/ga4/test", bytes.NewBufferString(`{"credentials": {"measurement_id": "G-ABC"}}`))
	reqGA4.Header.Set("Content-Type", "application/json")
	respGA4, _ := app.Test(reqGA4)
	if respGA4.StatusCode != fiber.StatusBadRequest {
		t.Errorf("GA4 sem api_secret deveria retornar 400, obteve %d", respGA4.StatusCode)
	}

	// 3. Google Ads sem endpoint_url
	reqAds := httptest.NewRequest("POST", "/api/v1/sites/"+validUUID+"/integrations/google_ads/test", bytes.NewBufferString(`{"credentials": {"conversion_action": "lead"}}`))
	reqAds.Header.Set("Content-Type", "application/json")
	respAds, _ := app.Test(reqAds)
	if respAds.StatusCode != fiber.StatusBadRequest {
		t.Errorf("Google Ads sem endpoint deveria retornar 400, obteve %d", respAds.StatusCode)
	}

	// 4. LinkedIn CAPI sem access_token
	reqLI := httptest.NewRequest("POST", "/api/v1/sites/"+validUUID+"/integrations/linkedin_capi/test", bytes.NewBufferString(`{"credentials": {"conversion_rule_id": "12345"}}`))
	reqLI.Header.Set("Content-Type", "application/json")
	respLI, _ := app.Test(reqLI)
	if respLI.StatusCode != fiber.StatusBadRequest {
		t.Errorf("LinkedIn sem access_token deveria retornar 400, obteve %d", respLI.StatusCode)
	}
}

func TestHandleSiteKeys_Validation(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	h := NewIntegrationsHandler(cfg, nil, nil)

	app := fiber.New()
	app.Get("/api/v1/sites/:site_id/keys", h.HandleListSiteKeys)
	app.Post("/api/v1/sites/:site_id/keys", h.HandleCreateSiteKey)
	app.Post("/api/v1/sites/:site_id/keys/:key_id/revoke", h.HandleRevokeSiteKey)

	// 1. Rejeita site_id inválido na listagem
	reqList := httptest.NewRequest("GET", "/api/v1/sites/invalid-uuid/keys", nil)
	respList, _ := app.Test(reqList)
	if respList.StatusCode != fiber.StatusBadRequest {
		t.Errorf("esperado 400 para site_id inválido, obteve %d", respList.StatusCode)
	}

	// 2. Rejeita site_id inválido na criação
	reqCreate := httptest.NewRequest("POST", "/api/v1/sites/invalid-uuid/keys", bytes.NewBufferString(`{"name":"GTM"}`))
	reqCreate.Header.Set("Content-Type", "application/json")
	respCreate, _ := app.Test(reqCreate)
	if respCreate.StatusCode != fiber.StatusBadRequest {
		t.Errorf("esperado 400 para site_id inválido na criação, obteve %d", respCreate.StatusCode)
	}

	// 3. Rejeita key_id inválido na revogação
	reqRevoke := httptest.NewRequest("POST", "/api/v1/sites/22222222-2222-2222-2222-222222222222/keys/invalid-key-uuid/revoke", nil)
	respRevoke, _ := app.Test(reqRevoke)
	if respRevoke.StatusCode != fiber.StatusBadRequest {
		t.Errorf("esperado 400 para key_id inválido na revogação, obteve %d", respRevoke.StatusCode)
	}
}

func TestHandleTestIntegration_LinkedIn_ResponseStructure(t *testing.T) {
	resp := TestIntegrationResponse{
		Success:           false,
		StatusCode:        401,
		Platform:          "linkedin_capi",
		LatencyMs:         150,
		ResponseBody:      `{"status":401,"message":"Invalid access token"}`,
		FriendlyTitle:     "Token de Acesso Inválido ou Expirado",
		FriendlyMessage:   "O LinkedIn rejeitou a autenticação do token informado.",
		ActionAdvice:      "Gere um novo token no Signals Manager.",
		PropagationNotice: "",
	}

	body, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Erro ao serializar TestIntegrationResponse: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("Erro ao deserializar TestIntegrationResponse: %v", err)
	}

	if parsed["friendly_title"] != "Token de Acesso Inválido ou Expirado" {
		t.Errorf("friendly_title inesperado: %v", parsed["friendly_title"])
	}
	if parsed["action_advice"] != "Gere um novo token no Signals Manager." {
		t.Errorf("action_advice inesperado: %v", parsed["action_advice"])
	}
}

func TestHandleGetRoutingSummary_Fallback(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	h := NewIntegrationsHandler(cfg, nil, nil) // redis nil -> fallback suave

	app := fiber.New()
	app.Get("/api/v1/sites/:site_id/routing-summary", h.HandleGetRoutingSummary)

	// Invalido
	reqBad := httptest.NewRequest("GET", "/api/v1/sites/invalid:site-id/routing-summary", nil)
	respBad, _ := app.Test(reqBad)
	if respBad.StatusCode != fiber.StatusBadRequest {
		t.Errorf("esperava status 400 para site_id invalido, obteve %d", respBad.StatusCode)
	}

	// Valido global
	req := httptest.NewRequest("GET", "/api/v1/sites/global/routing-summary", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("erro na requisicao: %v", err)
	}

	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("esperado status 200, obtido %d", resp.StatusCode)
	}

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatalf("erro ao decodificar resposta: %v", err)
	}

	if data["total"] != float64(0) {
		t.Errorf("esperado total=0, obtido %v", data["total"])
	}
	if data["success_rate"] != "100.0" {
		t.Errorf("esperado success_rate='100.0', obtido %v", data["success_rate"])
	}
}

func TestHandleGetRoutingRecent_Fallback(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	h := NewIntegrationsHandler(cfg, nil, nil) // redis nil -> lista vazia

	app := fiber.New()
	app.Get("/api/v1/sites/:site_id/routing-recent", h.HandleGetRoutingRecent)

	// Invalido
	reqBad := httptest.NewRequest("GET", "/api/v1/sites/invalid:recent/routing-recent", nil)
	respBad, _ := app.Test(reqBad)
	if respBad.StatusCode != fiber.StatusBadRequest {
		t.Errorf("esperava status 400 para site_id invalido, obteve %d", respBad.StatusCode)
	}

	// Valido UUID
	req := httptest.NewRequest("GET", "/api/v1/sites/22222222-2222-2222-2222-222222222222/routing-recent", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("erro na requisicao: %v", err)
	}

	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("esperado status 200, obtido %d", resp.StatusCode)
	}

	var items []interface{}
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("erro ao decodificar resposta: %v", err)
	}

	if len(items) != 0 {
		t.Errorf("esperado 0 items, obtido %d", len(items))
	}
}

func TestHandleSaveIntegration_WhitelistValidation(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	h := NewIntegrationsHandler(cfg, nil, nil)

	app := fiber.New()
	app.Put("/api/v1/sites/:site_id/integrations/:platform", h.HandleSaveIntegration)

	validUUID := "22222222-2222-2222-2222-222222222222"

	// 1. Chave não permitida em credentials
	reqBody := `{"is_active": true, "credentials": {"junk_param": "val"}}`
	req := httptest.NewRequest("PUT", "/api/v1/sites/"+validUUID+"/integrations/meta_capi", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Errorf("esperava 400 para chave desconhecida em credentials, obteve %d", resp.StatusCode)
	}

	// 2. Chave não permitida em delete_keys
	reqBodyDel := `{"is_active": true, "credentials": {}, "delete_keys": ["unsupported_secret"]}`
	reqDel := httptest.NewRequest("PUT", "/api/v1/sites/"+validUUID+"/integrations/meta_capi", bytes.NewBufferString(reqBodyDel))
	reqDel.Header.Set("Content-Type", "application/json")
	respDel, _ := app.Test(reqDel)
	if respDel.StatusCode != fiber.StatusBadRequest {
		t.Errorf("esperava 400 para chave desconhecida em delete_keys, obteve %d", respDel.StatusCode)
	}
}

func TestHandleSaveIntegration_ConflictValidation(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	h := NewIntegrationsHandler(cfg, nil, nil)

	app := fiber.New()
	app.Put("/api/v1/sites/:site_id/integrations/:platform", h.HandleSaveIntegration)

	validUUID := "22222222-2222-2222-2222-222222222222"

	// Conflito entre credentials e delete_keys
	reqBody := `{"is_active": true, "credentials": {"access_token": "abc1234"}, "delete_keys": ["access_token"]}`
	req := httptest.NewRequest("PUT", "/api/v1/sites/"+validUUID+"/integrations/meta_capi", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Errorf("esperava 400 para conflito entre credentials e delete_keys, obteve %d", resp.StatusCode)
	}

	var data map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&data)
	if errMsg, ok := data["error"].(string); !ok || !strings.Contains(errMsg, "conflito de chave") {
		t.Errorf("esperava mensagem contendo 'conflito de chave', obteve: %v", data["error"])
	}
}

func TestKeyRing_IntegrationsHandler_EncryptionAndFallback(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	h := NewIntegrationsHandler(cfg, nil, nil)

	kr := h.getKeyRing()
	if kr == nil {
		t.Fatal("esperava keyring não nulo com fallback determinístico")
	}

	siteUUID := "c56a4180-65aa-42ec-a945-5fd2dec0538d"
	token := "EAABsecret123456"
	encrypted, err := kr.EncryptField(token, siteUUID, "meta_capi", "access_token")
	if err != nil {
		t.Fatalf("falha ao criptografar token: %v", err)
	}

	decrypted, err := kr.DecryptField(encrypted, siteUUID, "meta_capi", "access_token")
	if err != nil {
		t.Fatalf("falha ao decifrar token: %v", err)
	}
	if decrypted != token {
		t.Fatalf("esperava %s, obteve %s", token, decrypted)
	}
}




