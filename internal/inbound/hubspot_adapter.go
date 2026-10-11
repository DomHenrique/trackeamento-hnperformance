package inbound

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

type HubSpotAdapter struct{}

func (a *HubSpotAdapter) Parse(c *fiber.Ctx, body []byte) (*NormalizedCRMEvent, error) {
	// HubSpot pode enviar array de eventos ou um único objeto JSON
	var rawObj map[string]interface{}
	if err := json.Unmarshal(body, &rawObj); err != nil {
		// Tenta desempacotar como array
		var rawArr []map[string]interface{}
		if errArr := json.Unmarshal(body, &rawArr); errArr == nil && len(rawArr) > 0 {
			rawObj = rawArr[0]
		} else {
			return nil, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
		}
	}

	norm := &NormalizedCRMEvent{
		Provider:   "hubspot",
		EventName:  "purchase",
		Currency:   "BRL",
		OccurredAt: time.Now(),
		RawPayload: rawObj,
		CustomData: make(map[string]interface{}),
	}

	// 1. Extração de propriedades do Deal / Contact
	if props, ok := rawObj["properties"].(map[string]interface{}); ok {
		for k, v := range props {
			valStr := extractHubSpotPropValue(v)
			switch strings.ToLower(k) {
			case "amount", "hs_deal_amount", "closed_amount":
				norm.Value = toFloat64(valStr)
			case "dealstage":
				norm.Status = valStr
			case "email":
				norm.Email = strings.TrimSpace(valStr)
			case "phone", "mobilephone", "hs_lead_phone":
				norm.Phone = strings.TrimSpace(valStr)
			case "dealname", "deal_name":
				norm.Name = valStr
			default:
				norm.CustomData[k] = valStr
			}
		}
	}

	// 2. Extração de contatos associados
	if contact, ok := rawObj["associated_contact"].(map[string]interface{}); ok {
		if email, ok := contact["email"].(string); ok && norm.Email == "" {
			norm.Email = strings.TrimSpace(email)
		}
		if phone, ok := contact["phone"].(string); ok && norm.Phone == "" {
			norm.Phone = strings.TrimSpace(phone)
		}
		if name, ok := contact["name"].(string); ok && norm.Name == "" {
			norm.Name = strings.TrimSpace(name)
		}
	}

	// 3. Propriedades na raiz do JSON
	if norm.Email == "" {
		if e, ok := rawObj["email"].(string); ok {
			norm.Email = strings.TrimSpace(e)
		}
	}
	if norm.Phone == "" {
		if p, ok := rawObj["phone"].(string); ok {
			norm.Phone = strings.TrimSpace(p)
		} else if p, ok := rawObj["mobilephone"].(string); ok {
			norm.Phone = strings.TrimSpace(p)
		}
	}
	if norm.Value == 0 {
		if v, ok := rawObj["amount"]; ok {
			norm.Value = toFloat64(v)
		}
	}
	if norm.Status == "" {
		if s, ok := rawObj["dealstage"].(string); ok {
			norm.Status = s
		} else if s, ok := rawObj["propertyValue"].(string); ok {
			norm.Status = s
		}
	}

	// Order ID / Deal ID
	if id, ok := rawObj["dealId"].(string); ok && id != "" {
		norm.OrderID = id
	} else if idNum, ok := rawObj["dealId"].(float64); ok && idNum > 0 {
		norm.OrderID = fmt.Sprintf("%.0f", idNum)
	} else if objID, ok := rawObj["objectId"].(float64); ok && objID > 0 {
		norm.OrderID = fmt.Sprintf("%.0f", objID)
	}

	if norm.OrderID == "" {
		norm.OrderID = fmt.Sprintf("hs_%d", time.Now().UnixNano())
	}

	if norm.Email == "" && norm.Phone == "" {
		return nil, ErrNoContactIdentifier
	}

	return norm, nil
}

func extractHubSpotPropValue(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	case float64:
		return fmt.Sprintf("%v", val)
	case map[string]interface{}:
		if innerVal, ok := val["value"]; ok {
			return fmt.Sprintf("%v", innerVal)
		}
	}
	return ""
}
