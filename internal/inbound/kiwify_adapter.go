package inbound

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

type KiwifyAdapter struct{}

func (a *KiwifyAdapter) Parse(c *fiber.Ctx, body []byte) (*NormalizedCRMEvent, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}

	norm := &NormalizedCRMEvent{
		Provider:   "kiwify",
		EventName:  "purchase",
		Currency:   "BRL",
		OccurredAt: time.Now(),
		RawPayload: raw,
		CustomData: make(map[string]interface{}),
	}

	if st, ok := raw["order_status"].(string); ok {
		norm.Status = st
	}

	if id, ok := raw["order_id"].(string); ok && id != "" {
		norm.OrderID = id
	}

	// Valor
	if comms, ok := raw["Commissions"].(map[string]interface{}); ok {
		if charge, ok := comms["charge_amount"]; ok {
			v := toFloat64(charge)
			// Se o valor estiver em centavos (> 100 sem casa decimal)
			if v > 100 && strings.Contains(fmt.Sprintf("%v", charge), ".") == false {
				v = v / 100.0
			}
			norm.Value = v
		}
		if cur, ok := comms["currency"].(string); ok && cur != "" {
			norm.Currency = cur
		}
	} else if val, ok := raw["order_value"]; ok {
		norm.Value = toFloat64(val)
	}

	// Customer
	if cust, ok := raw["Customer"].(map[string]interface{}); ok {
		if email, ok := cust["email"].(string); ok {
			norm.Email = strings.TrimSpace(email)
		}
		if mobile, ok := cust["mobile"].(string); ok {
			norm.Phone = strings.TrimSpace(mobile)
		} else if phone, ok := cust["phone"].(string); ok {
			norm.Phone = strings.TrimSpace(phone)
		}
		if name, ok := cust["full_name"].(string); ok {
			norm.Name = strings.TrimSpace(name)
		} else if name, ok := cust["name"].(string); ok {
			norm.Name = strings.TrimSpace(name)
		}
	}

	// Tracking parameters de Kiwify (src, sck, etc.)
	if trk, ok := raw["tracking"].(map[string]interface{}); ok {
		for k, v := range trk {
			norm.CustomData["tracking_"+k] = v
			if k == "sck" || k == "src" {
				if s, ok := v.(string); ok && strings.HasPrefix(s, "hn_vis_") {
					norm.VisitorID = s
				}
			}
		}
	}

	// Fallback raiz
	if norm.Email == "" {
		if e, ok := raw["email"].(string); ok {
			norm.Email = strings.TrimSpace(e)
		}
	}
	if norm.Phone == "" {
		if p, ok := raw["mobile"].(string); ok {
			norm.Phone = strings.TrimSpace(p)
		} else if p, ok := raw["phone"].(string); ok {
			norm.Phone = strings.TrimSpace(p)
		}
	}

	if norm.OrderID == "" {
		norm.OrderID = fmt.Sprintf("kiwify_%d", time.Now().UnixNano())
	}

	if norm.Email == "" && norm.Phone == "" {
		return nil, ErrNoContactIdentifier
	}

	return norm, nil
}
