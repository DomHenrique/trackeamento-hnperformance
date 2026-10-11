package inbound

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

type AsaasAdapter struct{}

func (a *AsaasAdapter) Parse(c *fiber.Ctx, body []byte) (*NormalizedCRMEvent, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}

	norm := &NormalizedCRMEvent{
		Provider:   "asaas",
		EventName:  "purchase",
		Currency:   "BRL",
		OccurredAt: time.Now(),
		RawPayload: raw,
		CustomData: make(map[string]interface{}),
	}

	eventStr, _ := raw["event"].(string)
	norm.Status = eventStr

	// Filtrar eventos não relacionados a pagamento confirmado/recebido se desejado,
	// mas deixamos tolerante caso o usuário queira rastrear PAYMENT_CREATED ou PAYMENT_CONFIRMED
	if eventStr != "" {
		norm.CustomData["asaas_event"] = eventStr
	}

	target := raw
	if payment, ok := raw["payment"].(map[string]interface{}); ok {
		target = payment
	}

	if id, ok := target["id"].(string); ok && id != "" {
		norm.OrderID = id
	}

	if v, ok := target["value"]; ok {
		norm.Value = toFloat64(v)
	} else if v, ok := target["netValue"]; ok {
		norm.Value = toFloat64(v)
	}

	if desc, ok := target["description"].(string); ok {
		norm.CustomData["description"] = desc
	}

	if billingType, ok := target["billingType"].(string); ok {
		norm.CustomData["billing_type"] = billingType
	}

	// Cliente nos dados aninhados ou na raiz
	if customer, ok := raw["customer"].(map[string]interface{}); ok {
		if email, ok := customer["email"].(string); ok {
			norm.Email = strings.TrimSpace(email)
		}
		if phone, ok := customer["mobilePhone"].(string); ok {
			norm.Phone = strings.TrimSpace(phone)
		} else if phone, ok := customer["phone"].(string); ok {
			norm.Phone = strings.TrimSpace(phone)
		}
		if name, ok := customer["name"].(string); ok {
			norm.Name = strings.TrimSpace(name)
		}
	}

	// Campos do cliente no target (payment) ou raiz
	if norm.Email == "" {
		if e, ok := target["customerEmail"].(string); ok {
			norm.Email = strings.TrimSpace(e)
		} else if e, ok := target["email"].(string); ok {
			norm.Email = strings.TrimSpace(e)
		} else if e, ok := raw["email"].(string); ok {
			norm.Email = strings.TrimSpace(e)
		}
	}
	if norm.Phone == "" {
		if p, ok := target["customerPhone"].(string); ok {
			norm.Phone = strings.TrimSpace(p)
		} else if p, ok := target["mobilePhone"].(string); ok {
			norm.Phone = strings.TrimSpace(p)
		} else if p, ok := raw["phone"].(string); ok {
			norm.Phone = strings.TrimSpace(p)
		}
	}
	if norm.Name == "" {
		if n, ok := target["customerName"].(string); ok {
			norm.Name = strings.TrimSpace(n)
		} else if n, ok := raw["name"].(string); ok {
			norm.Name = strings.TrimSpace(n)
		}
	}

	// Extração de visitor_id se passado em externalReference
	if extRef, ok := target["externalReference"].(string); ok && extRef != "" {
		norm.CustomData["external_reference"] = extRef
		if strings.HasPrefix(extRef, "hn_vis_") {
			norm.VisitorID = extRef
		}
	}

	if norm.OrderID == "" {
		norm.OrderID = fmt.Sprintf("asaas_%d", time.Now().UnixNano())
	}

	if norm.Email == "" && norm.Phone == "" {
		return nil, ErrNoContactIdentifier
	}

	return norm, nil
}
