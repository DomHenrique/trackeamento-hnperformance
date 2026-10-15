package collector

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"tracking-engine/internal/config"
)

func TestAnalyticsHandlers_ClickHouseNil(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	handler := NewHandler(cfg, nil, nil, nil)

	app := fiber.New()
	app.Get("/api/v1/analytics/funnel", handler.HandleAnalyticsFunnel)
	app.Get("/api/v1/analytics/attribution/paths", handler.HandleAnalyticsAttributionPaths)
	app.Get("/api/v1/analytics/visitor/journey", handler.HandleAnalyticsVisitorJourney)

	// Test Funnel when ClickHouse is unavailable
	reqFunnel := httptest.NewRequest(http.MethodGet, "/api/v1/analytics/funnel?range=7d", nil)
	respFunnel, err := app.Test(reqFunnel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if respFunnel.StatusCode != fiber.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", respFunnel.StatusCode)
	}

	// Test Paths when ClickHouse is unavailable
	reqPaths := httptest.NewRequest(http.MethodGet, "/api/v1/analytics/attribution/paths?range=7d", nil)
	respPaths, err := app.Test(reqPaths)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if respPaths.StatusCode != fiber.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", respPaths.StatusCode)
	}

	// Test Journey when ClickHouse is unavailable
	reqJourney := httptest.NewRequest(http.MethodGet, "/api/v1/analytics/visitor/journey?visitor_id=v_123", nil)
	respJourney, err := app.Test(reqJourney)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if respJourney.StatusCode != fiber.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", respJourney.StatusCode)
	}

	// Test Journey with missing visitor_id
	reqJourneyMissing := httptest.NewRequest(http.MethodGet, "/api/v1/analytics/visitor/journey", nil)
	respJourneyMissing, err := app.Test(reqJourneyMissing)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if respJourneyMissing.StatusCode != fiber.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", respJourneyMissing.StatusCode)
	}

	// Test HandleAnalyticsPages when ClickHouse is unavailable
	app.Get("/api/v1/analytics/pages", handler.HandleAnalyticsPages)
	reqPages := httptest.NewRequest(http.MethodGet, "/api/v1/analytics/pages?site_id=6ba7b810-9dad-11d1-80b4-00c04fd430c8&range=7d", nil)
	respPages, err := app.Test(reqPages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if respPages.StatusCode != fiber.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", respPages.StatusCode)
	}

	// Test HandleSitemapSync when Postgres is unavailable
	app.Post("/api/v1/sites/:id/sitemap-sync", handler.HandleSitemapSync)
	reqSync := httptest.NewRequest(http.MethodPost, "/api/v1/sites/site_123/sitemap-sync", nil)
	respSync, err := app.Test(reqSync)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if respSync.StatusCode != fiber.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", respSync.StatusCode)
	}

	// Test HandleGetSiteCatalog when Postgres is unavailable
	app.Get("/api/v1/sites/:id/catalog", handler.HandleGetSiteCatalog)
	reqCatalog := httptest.NewRequest(http.MethodGet, "/api/v1/sites/site_123/catalog", nil)
	respCatalog, err := app.Test(reqCatalog)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if respCatalog.StatusCode != fiber.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", respCatalog.StatusCode)
	}
}

func TestAnalyticsHandlers_SiteIDValidation(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	handler := NewHandler(cfg, nil, nil, nil)

	app := fiber.New()
	app.Get("/api/v1/analytics/overview", handler.HandleAnalyticsOverview)
	app.Get("/api/v1/analytics/pages", handler.HandleAnalyticsPages)
	app.Get("/api/v1/analytics/leads", handler.HandleAnalyticsLeads)
	app.Get("/api/v1/analytics/export/leads.csv", handler.HandleExportLeadsCSV)
	app.Get("/api/v1/analytics/funnel", handler.HandleAnalyticsFunnel)
	app.Get("/api/v1/analytics/attribution/paths", handler.HandleAnalyticsAttributionPaths)
	app.Get("/api/v1/analytics/visitor/journey", handler.HandleAnalyticsVisitorJourney)

	endpoints := []string{
		"/api/v1/analytics/overview",
		"/api/v1/analytics/pages",
		"/api/v1/analytics/leads",
		"/api/v1/analytics/export/leads.csv",
		"/api/v1/analytics/funnel",
		"/api/v1/analytics/attribution/paths",
		"/api/v1/analytics/visitor/journey?visitor_id=v_123",
	}

	invalidSiteIDs := []string{
		"not-a-uuid",
		"12345",
		"6ba7b810-9dad-11d1-80b4-00c04fd430c8:griddmkt360.com.br",
		"invalid-site-id",
	}

	for _, ep := range endpoints {
		sep := "?"
		if strings.Contains(ep, "?") {
			sep = "&"
		}

		// 1. Deve rejeitar site_id inválido com HTTP 400 Bad Request
		for _, invalidID := range invalidSiteIDs {
			url := ep + sep + "site_id=" + invalidID
			req := httptest.NewRequest(http.MethodGet, url, nil)
			resp, err := app.Test(req)
			if err != nil {
				t.Fatalf("erro inesperado na requisição para %s: %v", url, err)
			}
			if resp.StatusCode != fiber.StatusBadRequest {
				t.Errorf("endpoint %s com site_id=%s deveria retornar 400, mas retornou %d", ep, invalidID, resp.StatusCode)
			}
		}

		// 2. Com site_id válido ou vazio, a validação passa (retornando 503 pois ClickHouse é nil)
		validUUID := "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
		for _, validID := range []string{"", validUUID} {
			url := ep
			if validID != "" {
				url += sep + "site_id=" + validID
			}
			req := httptest.NewRequest(http.MethodGet, url, nil)
			resp, err := app.Test(req)
			if err != nil {
				t.Fatalf("erro inesperado na requisição para %s: %v", url, err)
			}
			if resp.StatusCode != fiber.StatusServiceUnavailable {
				t.Errorf("endpoint %s com site_id=%s deveria passar validação e retornar 503 (ClickHouse nil), retornou %d", ep, validID, resp.StatusCode)
			}
		}
	}
}
