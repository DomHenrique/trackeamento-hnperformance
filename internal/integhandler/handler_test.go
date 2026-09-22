package integhandler

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"tracking-engine/internal/config"
)

func TestMaskToken(t *testing.T) {
	short := "12345"
	if masked := maskToken(short); masked != "••••••••" {
		t.Errorf("esperado '••••••••', obtido '%s'", masked)
	}

	longToken := "EAAGm0PX9123456789abcdef"
	masked := maskToken(longToken)
	if masked != "EAAG••••••••cdef" {
		t.Errorf("esperado 'EAAG••••••••cdef', obtido '%s'", masked)
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

