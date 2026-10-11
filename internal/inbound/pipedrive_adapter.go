package inbound

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

type PipedriveAdapter struct{}

func (a *PipedriveAdapter) Parse(c *fiber.Ctx, body []byte) (*NormalizedCRMEvent, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}

	norm := &NormalizedCRMEvent{
		Provider:   "pipedrive",
		EventName:  "purchase",
		Currency:   "BRL",
		OccurredAt: time.Now(),
		RawPayload: raw,
		CustomData: make(map[string]interface{}),
	}

	// Pipedrive costuma enviar os dados em "current"
	target := raw
	if current, ok := raw["current"].(map[string]interface{}); ok {
		target = current
	}

	if status, ok := target["status"].(string); ok {
		norm.Status = status
		// Se o webhook for de atualização de deal mas não for won, ainda podemos registrar o status ou classificar
		if strings.ToLower(status) == "won" {
			norm.EventName = "deal_won"
		}
	}

	if id, ok := target["id"].(float64); ok && id > 0 {
		norm.OrderID = fmt.Sprintf("%.0f", id)
	} else if idStr, ok := target["id"].(string); ok && idStr != "" {
		norm.OrderID = idStr
	}

	if title, ok := target["title"].(string); ok {
		norm.Name = title
	}

	if val, ok := target["value"]; ok {
		norm.Value = toFloat64(val)
	}

	if curr, ok := target["currency"].(string); ok && curr != "" {
		norm.Currency = curr
	}

	// Extração de Person (Email / Phone / Name)
	if personRaw, ok := target["person_id"]; ok {
		if personMap, ok := personRaw.(map[string]interface{}); ok {
			if name, ok := personMap["name"].(string); ok && name != "" {
				norm.Name = name
			}
			norm.Email = extractPipedriveArrayValue(personMap["email"])
			norm.Phone = extractPipedriveArrayValue(personMap["phone"])
		}
	}

	// Fallback para campos diretos de email/phone caso person_id seja apenas um ID numérico
	if norm.Email == "" {
		if e, ok := target["person_email"].(string); ok {
			norm.Email = strings.TrimSpace(e)
		} else if e, ok := target["email"].(string); ok {
			norm.Email = strings.TrimSpace(e)
		}
	}
	if norm.Phone == "" {
		if p, ok := target["person_phone"].(string); ok {
			norm.Phone = strings.TrimSpace(p)
		} else if p, ok := target["phone"].(string); ok {
			norm.Phone = strings.TrimSpace(p)
		}
	}

	if norm.OrderID == "" {
		norm.OrderID = fmt.Sprintf("pd_%d", time.Now().UnixNano())
	}

	if norm.Email == "" && norm.Phone == "" {
		return nil, ErrNoContactIdentifier
	}

	return norm, nil
}

func extractPipedriveArrayValue(val interface{}) string {
	if val == nil {
		return ""
	}
	if str, ok := val.(string); ok {
		return strings.TrimSpace(str)
	}
	if arr, ok := val.([]interface{}); ok && len(arr) > 0 {
		for _, item := range arr {
			if itemMap, ok := item.(map[string]interface{}); ok {
				if v, ok := itemMap["value"].(string); ok && v != "" {
					return strings.TrimSpace(v)
				}
			} else if itemStr, ok := item.(string); ok && itemStr != "" {
				return strings.TrimSpace(itemStr)
			}
		}
	}
	return ""
}
