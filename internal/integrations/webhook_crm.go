package integrations

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
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
	SchemaVersion   int                    `json:"schema_version"`
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

// ComputeWebhookSignature calcula a assinatura HMAC-SHA256 sobre timestamp + "." + body
func ComputeWebhookSignature(secretToken, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secretToken))
	mac.Write([]byte(timestamp + "." + string(body)))
	return hex.EncodeToString(mac.Sum(nil))
}

// AllowedContactKeys whitelist de campos de contato transferíveis para CRM
var AllowedContactKeys = map[string]bool{
	"email":      true,
	"phone":      true,
	"name":       true,
	"first_name": true,
	"last_name":  true,
	"document":   true,
	"cpf":        true,
	"city":       true,
	"state":      true,
	"country":    true,
	"zip_code":   true,
	"company":    true,
}

// extractStringValue converte de forma segura números (float64, int, json.Number) ou strings
func extractStringValue(v interface{}) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return strings.TrimSpace(val)
	case float64:
		return fmt.Sprintf("%.0f", val)
	case float32:
		return fmt.Sprintf("%.0f", val)
	case int:
		return strconv.Itoa(val)
	case int64:
		return strconv.FormatInt(val, 10)
	case json.Number:
		return strings.TrimSpace(val.String())
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", val))
	}
}

// SendWebhook despacha o evento enriquecido de lead para o CRM / n8n
func (w *CRMWebhook) SendWebhook(ctx context.Context, webhookURL, secretToken string, ev *collector.EventPayload, firstTouch map[string]interface{}) error {
	if webhookURL == "" {
		return nil
	}

	contact := make(map[string]interface{})
	if ev.UserData != nil {
		for k, v := range ev.UserData {
			lowerK := strings.ToLower(strings.TrimSpace(k))
			if AllowedContactKeys[lowerK] {
				contact[lowerK] = v
			}
		}

		if rawEmail := extractStringValue(ev.UserData["email"]); rawEmail != "" {
			contact["email_normalized"] = identity.NormalizeEmail(rawEmail)
		}
		if rawPhone := extractStringValue(ev.UserData["phone"]); rawPhone != "" {
			contact["phone_normalized"] = identity.NormalizePhone(rawPhone)
		}
	}

	var fbp, fbc string
	if ev.UserData != nil {
		fbp = extractStringValue(ev.UserData["fbp"])
		fbc = extractStringValue(ev.UserData["fbc"])
	}
	if fbc == "" && ev.Attribution.FBCLID != "" {
		fbc = fmt.Sprintf("fb.1.%d.%s", time.Now().Unix(), ev.Attribution.FBCLID)
	}

	conversionTouch := map[string]interface{}{
		"url":          ev.Attribution.PageURL,
		"landing_page": ev.Attribution.LandingPage,
		"referrer":     ev.Attribution.Referrer,
		"utm_source":   ev.Attribution.UTMSource,
		"utm_medium":   ev.Attribution.UTMMedium,
		"utm_campaign": ev.Attribution.UTMCampaign,
		"utm_term":     ev.Attribution.UTMTerm,
		"utm_content":  ev.Attribution.UTMContent,
		"gclid":        ev.Attribution.GCLID,
		"gbraid":       ev.Attribution.GBRAID,
		"wbraid":       ev.Attribution.WBRAID,
		"fbclid":       ev.Attribution.FBCLID,
		"ttclid":       ev.Attribution.TTCLID,
		"fbp":          fbp,
		"fbc":          fbc,
		"ip":           ev.IPAddress,
		"user_agent":   ev.UserAgent,
		"device_type":  ev.DeviceType,
	}

	// Se firstTouch estiver vazio, marshals como null
	var ft map[string]interface{}
	if len(firstTouch) > 0 {
		ft = firstTouch
	}

	eventTime := ev.EventTime.UTC()
	if eventTime.IsZero() {
		eventTime = time.Now().UTC()
	}

	payload := WebhookPayload{
		SchemaVersion:   1,
		EventName:       ev.EventName,
		EventID:         ev.EventID,
		VisitorID:       ev.VisitorID,
		SessionID:       ev.SessionID,
		Timestamp:       eventTime,
		Contact:         contact,
		FirstTouch:      ft,
		ConversionTouch: conversionTouch,
		CustomData:      ev.CustomData,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("erro ao serializar webhook CRM payload: %w", err)
	}

	headers := map[string]string{
		"User-Agent":      "HN-Tracking-Engine/1.0",
		"Idempotency-Key": ev.EventID,
	}

	if secretToken != "" {
		ts := strconv.FormatInt(time.Now().UTC().Unix(), 10)
		headers["X-HN-Timestamp"] = ts
		headers["X-HN-Signature"] = ComputeWebhookSignature(secretToken, ts, body)
		headers["Authorization"] = "Bearer " + secretToken
	}

	_, _, err = w.http.PostWithRetry(ctx, webhookURL, headers, body, 5)
	return err
}
