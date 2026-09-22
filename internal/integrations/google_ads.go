package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"tracking-engine/internal/collector"
	"tracking-engine/internal/identity"
)

type GoogleAds struct {
	http *HTTPClient
}

func NewGoogleAds(client *HTTPClient) *GoogleAds {
	return &GoogleAds{http: client}
}

type GoogleAdsConversion struct {
	ConversionAction string                 `json:"conversion_action"`
	ConversionTime   string                 `json:"conversion_time"`
	GCLID            string                 `json:"gclid,omitempty"`
	GBRAID           string                 `json:"gbraid,omitempty"`
	WBRAID           string                 `json:"wbraid,omitempty"`
	HashedEmail      string                 `json:"hashed_email,omitempty"`
	HashedPhone      string                 `json:"hashed_phone,omitempty"`
	Value            float64                `json:"conversion_value,omitempty"`
	Currency         string                 `json:"currency_code,omitempty"`
	CustomData       map[string]interface{} `json:"custom_data,omitempty"`
}

// SendConversion formata e encaminha a conversão offline / Enhanced Conversion
func (g *GoogleAds) SendConversion(ctx context.Context, endpointURL string, apiKey string, ev *collector.EventPayload) error {
	if endpointURL == "" {
		return nil // Se não houver endpoint configurado, conclui sem erro
	}

	var hashedEmail, hashedPhone string
	if ev.UserData != nil {
		if em, ok := ev.UserData["email"].(string); ok && em != "" {
			hashedEmail = identity.HashSHA256(identity.NormalizeEmail(em))
		}
		if ph, ok := ev.UserData["phone"].(string); ok && ph != "" {
			hashedPhone = identity.HashSHA256(identity.NormalizePhone(ph))
		}
	}

	conv := GoogleAdsConversion{
		ConversionAction: ev.EventName,
		ConversionTime:   ev.EventTime.Format("2006-01-02 15:04:05-07:00"),
		GCLID:            ev.Attribution.GCLID,
		GBRAID:           ev.Attribution.GBRAID,
		WBRAID:           ev.Attribution.WBRAID,
		HashedEmail:      hashedEmail,
		HashedPhone:      hashedPhone,
		Currency:         "BRL",
		CustomData:       ev.CustomData,
	}

	if val, ok := ev.CustomData["value"].(float64); ok {
		conv.Value = val
	}

	body, err := json.Marshal(conv)
	if err != nil {
		return fmt.Errorf("erro ao serializar Google Ads conversion: %w", err)
	}

	headers := map[string]string{}
	if apiKey != "" {
		headers["Authorization"] = "Bearer " + apiKey
	}

	_, _, err = g.http.PostWithRetry(ctx, endpointURL, headers, body, 3)
	return err
}

// TestPing realiza um envio sintético para o endpoint do Google Ads / Enhanced Conversions
func (g *GoogleAds) TestPing(ctx context.Context, endpointURL, apiKey, conversionAction string) ([]byte, int, error) {
	if endpointURL == "" {
		return nil, 0, fmt.Errorf("endpoint_url é obrigatório para o Google Ads")
	}

	if conversionAction == "" {
		conversionAction = "test_conversion"
	}

	conv := GoogleAdsConversion{
		ConversionAction: conversionAction,
		ConversionTime:   time.Now().Format("2006-01-02 15:04:05-07:00"),
		GCLID:            "test_gclid_1234567890",
		HashedEmail:      identity.HashSHA256(identity.NormalizeEmail("teste@trackeamentohn.com.br")),
		HashedPhone:      identity.HashSHA256(identity.NormalizePhone("+5511999999999")),
		Currency:         "BRL",
		Value:            1.00,
		CustomData: map[string]interface{}{
			"test_mode": true,
			"source":    "TrackeamentoHN Wizard",
		},
	}

	body, err := json.Marshal(conv)
	if err != nil {
		return nil, 0, fmt.Errorf("erro ao serializar Google Ads payload de teste: %w", err)
	}

	headers := map[string]string{}
	if apiKey != "" {
		headers["Authorization"] = "Bearer " + apiKey
	}

	return g.http.PostRaw(ctx, endpointURL, headers, body)
}

