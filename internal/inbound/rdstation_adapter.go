package inbound

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

type RDStationAdapter struct{}

func (a *RDStationAdapter) Parse(c *fiber.Ctx, body []byte) (*NormalizedCRMEvent, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}

	norm := &NormalizedCRMEvent{
		Provider:   "rdstation",
		EventName:  "purchase",
		Currency:   "BRL",
		OccurredAt: time.Now(),
		RawPayload: raw,
		CustomData: make(map[string]interface{}),
	}

	// 1. Formato com array "leads" (Webhook clássico RD Station Marketing)
	if leadsRaw, ok := raw["leads"]; ok {
		if leadsSlice, ok := leadsRaw.([]interface{}); ok && len(leadsSlice) > 0 {
			if lead, ok := leadsSlice[0].(map[string]interface{}); ok {
				extractRDLead(lead, norm)
			}
		}
	} else if payloadRaw, ok := raw["payload"]; ok {
		// 2. Formato CDP v2 ("event_type": "SALE", "payload": { ... })
		if payloadMap, ok := payloadRaw.(map[string]interface{}); ok {
			extractRDLead(payloadMap, norm)
		}
	} else {
		// 3. Formato plano
		extractRDLead(raw, norm)
	}

	if norm.Email == "" && norm.Phone == "" {
		return nil, ErrNoContactIdentifier
	}

	if norm.OrderID == "" {
		norm.OrderID = fmt.Sprintf("rd_%d", time.Now().UnixNano())
	}

	return norm, nil
}

func extractRDLead(lead map[string]interface{}, norm *NormalizedCRMEvent) {
	if email, ok := lead["email"].(string); ok && email != "" {
		norm.Email = strings.TrimSpace(email)
	}
	if name, ok := lead["name"].(string); ok && name != "" {
		norm.Name = strings.TrimSpace(name)
	}

	// Telefones (mobile_phone ou personal_phone)
	if mobile, ok := lead["mobile_phone"].(string); ok && mobile != "" {
		norm.Phone = strings.TrimSpace(mobile)
	} else if personal, ok := lead["personal_phone"].(string); ok && personal != "" {
		norm.Phone = strings.TrimSpace(personal)
	} else if phone, ok := lead["phone"].(string); ok && phone != "" {
		norm.Phone = strings.TrimSpace(phone)
	}

	// Status e oportunidade
	if status, ok := lead["lead_stage"].(string); ok {
		norm.Status = status
	} else if status, ok := lead["status"].(string); ok {
		norm.Status = status
	}

	// Valor da oportunidade / venda
	if valRaw, ok := lead["value"]; ok {
		norm.Value = toFloat64(valRaw)
	} else if valRaw, ok := lead["opportunity_value"]; ok {
		norm.Value = toFloat64(valRaw)
	} else if saleRaw, ok := lead["sale"]; ok {
		if saleMap, ok := saleRaw.(map[string]interface{}); ok {
			if v, ok := saleMap["value"]; ok {
				norm.Value = toFloat64(v)
			}
			if cur, ok := saleMap["currency"].(string); ok && cur != "" {
				norm.Currency = cur
			}
		}
	}

	// Custom fields / visitor_id
	if customFields, ok := lead["custom_fields"].(map[string]interface{}); ok {
		for k, v := range customFields {
			norm.CustomData[k] = v
			lowerK := strings.ToLower(k)
			if (strings.Contains(lowerK, "visitor") || strings.Contains(lowerK, "vis_id")) && norm.VisitorID == "" {
				if s, ok := v.(string); ok {
					norm.VisitorID = strings.TrimSpace(s)
				}
			}
		}
	}

	if id, ok := lead["id"].(string); ok && id != "" {
		norm.OrderID = id
	} else if idNum, ok := lead["id"].(float64); ok && idNum > 0 {
		norm.OrderID = fmt.Sprintf("%.0f", idNum)
	}
}

func toFloat64(val interface{}) float64 {
	switch v := val.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case string:
		clean := strings.TrimSpace(v)
		clean = strings.ReplaceAll(clean, "R$", "")
		clean = strings.ReplaceAll(clean, "$", "")
		clean = strings.TrimSpace(clean)
		if strings.Contains(clean, ",") && strings.Contains(clean, ".") {
			clean = strings.ReplaceAll(clean, ".", "")
			clean = strings.ReplaceAll(clean, ",", ".")
		} else if strings.Contains(clean, ",") {
			clean = strings.ReplaceAll(clean, ",", ".")
		}
		f, _ := strconv.ParseFloat(clean, 64)
		return f
	default:
		return 0
	}
}
