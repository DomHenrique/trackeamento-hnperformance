package collector

import (
	"net/http"
	"net/http/httptest"
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
	// Handler checks clickhouse nil first (503)
	if respJourneyMissing.StatusCode != fiber.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", respJourneyMissing.StatusCode)
	}
}
