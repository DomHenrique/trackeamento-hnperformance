package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"tracking-engine/internal/collector"
)

type GA4MP struct {
	http *HTTPClient
}

func NewGA4MP(client *HTTPClient) *GA4MP {
	return &GA4MP{http: client}
}

type GA4Payload struct {
	ClientID string     `json:"client_id"`
	Events   []GA4Event `json:"events"`
}

type GA4Event struct {
	Name   string                 `json:"name"`
	Params map[string]interface{} `json:"params"`
}

// SendEvent despacha evento analítico para o Google Analytics 4 via Measurement Protocol
func (g *GA4MP) SendEvent(ctx context.Context, measurementID, apiSecret string, ev *collector.EventPayload) error {
	if measurementID == "" || apiSecret == "" {
		return fmt.Errorf("measurement_id ou api_secret ausentes para GA4 MP")
	}

	clientID := strings.TrimPrefix(ev.VisitorID, "v_")

	params := map[string]interface{}{
		"page_location": ev.Attribution.PageURL,
		"page_referrer": ev.Attribution.Referrer,
		"session_id":    ev.SessionID,
	}

	if ev.Attribution.UTMSource != "" {
		params["campaign_source"] = ev.Attribution.UTMSource
		params["campaign_medium"] = ev.Attribution.UTMMedium
		params["campaign_name"] = ev.Attribution.UTMCampaign
	}
	if ev.Attribution.GCLID != "" {
		params["gclid"] = ev.Attribution.GCLID
	}

	for k, v := range ev.CustomData {
		params[k] = v
	}

	ga4Event := GA4Event{
		Name:   mapGA4EventName(ev.EventName),
		Params: params,
	}

	payload := GA4Payload{
		ClientID: clientID,
		Events:   []GA4Event{ga4Event},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("erro ao serializar GA4 payload: %w", err)
	}

	url := fmt.Sprintf("https://www.google-analytics.com/mp/collect?measurement_id=%s&api_secret=%s", measurementID, apiSecret)
	_, _, err = g.http.PostWithRetry(ctx, url, nil, body, 3)
	return err
}

// TestPing realiza um envio de validação para o endpoint de debug do GA4 (/debug/mp/collect) retornando a validação em JSON
func (g *GA4MP) TestPing(ctx context.Context, measurementID, apiSecret string) ([]byte, int, error) {
	if measurementID == "" || apiSecret == "" {
		return nil, 0, fmt.Errorf("measurement_id e api_secret são obrigatórios")
	}

	testPayload := GA4Payload{
		ClientID: fmt.Sprintf("test_client_%d", time.Now().Unix()),
		Events: []GA4Event{
			{
				Name: "page_view",
				Params: map[string]interface{}{
					"page_location": "https://trackeamento.hnperformancedigital.com.br/test",
					"page_title":    "Teste de Conexão - Trackeamento HN",
					"test_mode":     true,
				},
			},
		},
	}

	body, err := json.Marshal(testPayload)
	if err != nil {
		return nil, 0, fmt.Errorf("erro ao serializar GA4 payload: %w", err)
	}

	// O endpoint /debug/mp/collect do GA4 valida as credenciais e estrutura do evento sem poluir as métricas de produção
	url := fmt.Sprintf("https://www.google-analytics.com/debug/mp/collect?measurement_id=%s&api_secret=%s", measurementID, apiSecret)
	return g.http.PostRaw(ctx, url, nil, body)
}


func mapGA4EventName(name string) string {
	switch strings.ToLower(name) {
	case "lead":
		return "generate_lead"
	case "purchase":
		return "purchase"
	case "whatsapp_click":
		return "click_whatsapp"
	case "page_view":
		return "page_view"
	default:
		return name
	}
}
