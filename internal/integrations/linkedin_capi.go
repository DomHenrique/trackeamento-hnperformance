package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"tracking-engine/internal/collector"
	"tracking-engine/internal/identity"
)

const (
	LinkedInAPIVersion        = "202401"
	LinkedInRestliProtocolVer = "2.0.0"
	LinkedInConversionURL     = "https://api.linkedin.com/rest/conversionEvents"
)

type LinkedInCAPI struct {
	http *HTTPClient
}

func NewLinkedInCAPI(client *HTTPClient) *LinkedInCAPI {
	return &LinkedInCAPI{http: client}
}

type LinkedInUserId struct {
	IdType  string `json:"idType"`
	IdValue string `json:"idValue"`
}

type LinkedInUserInfo struct {
	FirstName   string `json:"firstName,omitempty"`
	LastName    string `json:"lastName,omitempty"`
	Title       string `json:"title,omitempty"`
	CompanyPage string `json:"companyPage,omitempty"`
	CountryCode string `json:"countryCode,omitempty"`
}

type LinkedInUser struct {
	UserIds  []LinkedInUserId  `json:"userIds,omitempty"`
	UserInfo *LinkedInUserInfo `json:"userInfo,omitempty"`
}

type LinkedInConversionValue struct {
	CurrencyCode string `json:"currencyCode"`
	Amount       string `json:"amount"`
}

type LinkedInConversionEvent struct {
	Conversion           string                   `json:"conversion"`
	ConversionHappenedAt int64                    `json:"conversionHappenedAt"`
	ConversionValue      *LinkedInConversionValue `json:"conversionValue,omitempty"`
	User                 LinkedInUser             `json:"user"`
	EventId              string                   `json:"eventId,omitempty"`
}

// FormatConversionURN garante que o conversion rule ID esteja formatado com o prefixo URN do LinkedIn
func FormatConversionURN(ruleID string) string {
	trimmed := strings.TrimSpace(ruleID)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "urn:lla:llaPartnerConversion:") {
		return trimmed
	}
	return fmt.Sprintf("urn:lla:llaPartnerConversion:%s", trimmed)
}

// SendEvent formata e envia uma conversão qualificada para a LinkedIn Conversions API
func (l *LinkedInCAPI) SendEvent(ctx context.Context, accessToken, conversionRuleID string, ev *collector.EventPayload) error {
	if strings.TrimSpace(accessToken) == "" {
		return fmt.Errorf("access_token ausente para LinkedIn Conversions API")
	}
	urn := FormatConversionURN(conversionRuleID)
	if urn == "" {
		return fmt.Errorf("conversion_rule_id ausente para LinkedIn Conversions API")
	}

	userIds := make([]LinkedInUserId, 0)
	if ev.UserData != nil {
		if em, ok := ev.UserData["email"].(string); ok && em != "" {
			norm := identity.NormalizeEmail(em)
			userIds = append(userIds, LinkedInUserId{
				IdType:  "SHA256_EMAIL",
				IdValue: identity.HashSHA256(norm),
			})
		}
	}

	eventTimeMs := ev.EventTime.UnixMilli()
	if eventTimeMs <= 0 {
		eventTimeMs = time.Now().UnixMilli()
	}

	eventPayload := LinkedInConversionEvent{
		Conversion:           urn,
		ConversionHappenedAt: eventTimeMs,
		User: LinkedInUser{
			UserIds: userIds,
		},
		EventId: ev.EventID,
	}

	// Se houver valor na conversão
	if ev.CustomData != nil {
		if val, exists := ev.CustomData["value"]; exists {
			currency := "BRL"
			if cur, ok := ev.CustomData["currency"].(string); ok && cur != "" {
				currency = strings.ToUpper(cur)
			}
			eventPayload.ConversionValue = &LinkedInConversionValue{
				CurrencyCode: currency,
				Amount:       fmt.Sprintf("%.2f", parseNumericValue(val)),
			}
		}
	}

	body, err := json.Marshal(eventPayload)
	if err != nil {
		return fmt.Errorf("erro serializando LinkedIn payload: %w", err)
	}

	headers := map[string]string{
		"Authorization":              "Bearer " + accessToken,
		"LinkedIn-Version":           LinkedInAPIVersion,
		"X-Restli-Protocol-Version":  LinkedInRestliProtocolVer,
	}

	_, _, err = l.http.PostWithRetry(ctx, LinkedInConversionURL, headers, body, 3)
	return err
}

// TestPing realiza um envio sintético para a LinkedIn Conversions API para validar conectividade e token
func (l *LinkedInCAPI) TestPing(ctx context.Context, accessToken, conversionRuleID string) ([]byte, int, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, 0, fmt.Errorf("access_token é obrigatório para testar o LinkedIn CAPI")
	}
	urn := FormatConversionURN(conversionRuleID)
	if urn == "" {
		// Se não foi informado, utiliza um URN sintético de teste
		urn = "urn:lla:llaPartnerConversion:0"
	}

	testEvent := LinkedInConversionEvent{
		Conversion:           urn,
		ConversionHappenedAt: time.Now().UnixMilli(),
		User: LinkedInUser{
			UserIds: []LinkedInUserId{
				{
					IdType:  "SHA256_EMAIL",
					IdValue: identity.HashSHA256("test@hnperformancedigital.com.br"),
				},
			},
		},
		EventId: fmt.Sprintf("test_li_%d", time.Now().UnixNano()),
	}

	body, err := json.Marshal(testEvent)
	if err != nil {
		return nil, 0, fmt.Errorf("erro serializando LinkedIn payload de teste: %w", err)
	}

	headers := map[string]string{
		"Authorization":             "Bearer " + accessToken,
		"LinkedIn-Version":          LinkedInAPIVersion,
		"X-Restli-Protocol-Version": LinkedInRestliProtocolVer,
	}

	return l.http.PostRaw(ctx, LinkedInConversionURL, headers, body)
}

func parseNumericValue(val interface{}) float64 {
	switch v := val.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	default:
		return 0.0
	}
}
