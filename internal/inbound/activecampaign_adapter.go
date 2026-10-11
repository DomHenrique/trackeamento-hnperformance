package inbound

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

type ActiveCampaignAdapter struct{}

func (a *ActiveCampaignAdapter) Parse(c *fiber.Ctx, body []byte) (*NormalizedCRMEvent, error) {
	norm := &NormalizedCRMEvent{
		Provider:   "activecampaign",
		EventName:  "purchase",
		Currency:   "BRL",
		OccurredAt: time.Now(),
		CustomData: make(map[string]interface{}),
	}

	contentType := ""
	if c != nil {
		contentType = string(c.Request().Header.ContentType())
	}

	// 1. Tratamento caso seja application/x-www-form-urlencoded (padrão de webhooks nativos do ActiveCampaign)
	if strings.Contains(contentType, "application/x-www-form-urlencoded") || (!strings.HasPrefix(strings.TrimSpace(string(body)), "{") && !strings.HasPrefix(strings.TrimSpace(string(body)), "[")) {
		values, err := url.ParseQuery(string(body))
		if err == nil && len(values) > 0 {
			rawMap := make(map[string]interface{})
			for k, v := range values {
				if len(v) > 0 {
					rawMap[k] = v[0]
				}
			}
			norm.RawPayload = rawMap

			// Email
			norm.Email = strings.TrimSpace(values.Get("contact[email]"))
			if norm.Email == "" {
				norm.Email = strings.TrimSpace(values.Get("email"))
			}

			// Phone
			norm.Phone = strings.TrimSpace(values.Get("contact[phone]"))
			if norm.Phone == "" {
				norm.Phone = strings.TrimSpace(values.Get("phone"))
			}

			// Name
			firstName := values.Get("contact[first_name]")
			lastName := values.Get("contact[last_name]")
			norm.Name = strings.TrimSpace(firstName + " " + lastName)

			// Deal info
			norm.OrderID = values.Get("deal[id]")
			if norm.OrderID == "" {
				norm.OrderID = values.Get("id")
			}
			dealValue := values.Get("deal[value]")
			if dealValue == "" {
				dealValue = values.Get("value")
			}
			norm.Value = toFloat64(dealValue)

			dealCurr := values.Get("deal[currency]")
			if dealCurr != "" {
				norm.Currency = strings.ToUpper(dealCurr)
			}

			status := values.Get("deal[status]")
			norm.Status = status
			if status == "1" || strings.ToLower(status) == "won" {
				norm.EventName = "deal_won"
			}
		}
	} else {
		// 2. Tratamento JSON
		var raw map[string]interface{}
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
		}
		norm.RawPayload = raw

		if deal, ok := raw["deal"].(map[string]interface{}); ok {
			if id, ok := deal["id"]; ok {
				norm.OrderID = fmt.Sprintf("%v", id)
			}
			if val, ok := deal["value"]; ok {
				norm.Value = toFloat64(val)
			}
			if cur, ok := deal["currency"].(string); ok && cur != "" {
				norm.Currency = strings.ToUpper(cur)
			}
			if st, ok := deal["status"].(string); ok {
				norm.Status = st
				if st == "1" || strings.ToLower(st) == "won" {
					norm.EventName = "deal_won"
				}
			}
		}

		if contact, ok := raw["contact"].(map[string]interface{}); ok {
			if e, ok := contact["email"].(string); ok {
				norm.Email = strings.TrimSpace(e)
			}
			if p, ok := contact["phone"].(string); ok {
				norm.Phone = strings.TrimSpace(p)
			}
			fn, _ := contact["first_name"].(string)
			ln, _ := contact["last_name"].(string)
			norm.Name = strings.TrimSpace(fn + " " + ln)
		}

		// Fallbacks na raiz
		if norm.Email == "" {
			if e, ok := raw["email"].(string); ok {
				norm.Email = strings.TrimSpace(e)
			}
		}
		if norm.Phone == "" {
			if p, ok := raw["phone"].(string); ok {
				norm.Phone = strings.TrimSpace(p)
			}
		}
		if norm.Value == 0 {
			if v, ok := raw["value"]; ok {
				norm.Value = toFloat64(v)
			}
		}
	}

	if norm.OrderID == "" {
		norm.OrderID = fmt.Sprintf("ac_%d", time.Now().UnixNano())
	}

	if norm.Email == "" && norm.Phone == "" {
		return nil, ErrNoContactIdentifier
	}

	return norm, nil
}
