package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"tracking-engine/internal/collector"
	"tracking-engine/internal/identity"
)

type CRMWebhook struct {
	http *HTTPClient
}

func NewCRMWebhook(client *HTTPClient) *CRMWebhook {
	return &CRMWebhook{http: client}
}

type WebhookPayload struct {
	EventName       string                 `json:"event_name"`
	EventID         string                 `json:"event_id"`
	VisitorID       string                 `json:"visitor_id"`
	SessionID       string                 `json:"session_id"`
	Timestamp       time.Time              `json:"timestamp"`
	Contact         map[string]interface{} `json:"contact"`
	FirstTouch      map[string]interface{} `json:"first_touch"`
	ConversionTouch map[string]interface{} `json:"conversion_touch"`
	CustomData      map[string]interface{} `json:"custom_data,omitempty"`
}

// SendWebhook despacha o evento enriquecido de lead para o CRM / n8n
func (w *CRMWebhook) SendWebhook(ctx context.Context, webhookURL, secretToken string, ev *collector.EventPayload, firstTouch map[string]interface{}) error {
	if webhookURL == "" {
		return nil
	}

	contact := make(map[string]interface{})
	if ev.UserData != nil {
		for k, v := range ev.UserData {
			contact[k] = v
		}
		if em, ok := ev.UserData["email"].(string); ok {
			contact["email_normalized"] = identity.NormalizeEmail(em)
		}
		if ph, ok := ev.UserData["phone"].(string); ok {
			contact["phone_normalized"] = identity.NormalizePhone(ph)
		}
	}

	conversionTouch := map[string]interface{}{
		"url":          ev.Attribution.PageURL,
		"landing_page": ev.Attribution.LandingPage,
		"referrer":     ev.Attribution.Referrer,
		"utm_source":   ev.Attribution.UTMSource,
		"utm_medium":   ev.Attribution.UTMMedium,
		"utm_campaign": ev.Attribution.UTMCampaign,
		"gclid":        ev.Attribution.GCLID,
		"fbclid":       ev.Attribution.FBCLID,
		"ip":           ev.IPAddress,
		"user_agent":   ev.UserAgent,
		"device_type":  ev.DeviceType,
	}

	payload := WebhookPayload{
		EventName:       ev.EventName,
		EventID:         ev.EventID,
		VisitorID:       ev.VisitorID,
		SessionID:       ev.SessionID,
		Timestamp:       ev.EventTime,
		Contact:         contact,
		FirstTouch:      firstTouch,
		ConversionTouch: conversionTouch,
		CustomData:      ev.CustomData,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("erro ao serializar webhook CRM payload: %w", err)
	}

	headers := map[string]string{
		"User-Agent": "HN-Tracking-Engine/1.0",
	}
	if secretToken != "" {
		headers["X-Webhook-Secret"] = secretToken
		headers["Authorization"] = "Bearer " + secretToken
	}

	_, _, err = w.http.PostWithRetry(ctx, webhookURL, headers, body, 5)
	return err
}
