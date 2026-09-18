package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
