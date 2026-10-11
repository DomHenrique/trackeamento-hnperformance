package inbound

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

var (
	emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)
)

type CustomAdapter struct{}

func (a *CustomAdapter) Parse(c *fiber.Ctx, body []byte) (*NormalizedCRMEvent, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		// Tenta array
		var rawArr []map[string]interface{}
		if errArr := json.Unmarshal(body, &rawArr); errArr == nil && len(rawArr) > 0 {
			raw = rawArr[0]
		} else {
			return nil, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
		}
	}

	norm := &NormalizedCRMEvent{
		Provider:   "custom",
		EventName:  "purchase",
		Currency:   "BRL",
		OccurredAt: time.Now(),
		RawPayload: raw,
		CustomData: make(map[string]interface{}),
	}

	// Varredura recursiva inteligente
	scanner := &smartScanner{
		visitedDepth: 0,
		maxDepth:     4,
	}
	scanner.scan(raw, norm)

	if norm.Email == "" && norm.Phone == "" {
		return nil, ErrNoContactIdentifier
	}

	if norm.OrderID == "" {
		norm.OrderID = fmt.Sprintf("cst_%d", time.Now().UnixNano())
	}

	// Ajuste semântico de EventName baseado em status se detectado
	stLower := strings.ToLower(norm.Status)
	if strings.Contains(stLower, "won") || strings.Contains(stLower, "ganh") {
		norm.EventName = "deal_won"
	} else if strings.Contains(stLower, "fatur") || strings.Contains(stLower, "pag") || strings.Contains(stLower, "aprov") {
		norm.EventName = "purchase"
	}

	return norm, nil
}

type smartScanner struct {
	visitedDepth int
	maxDepth     int
}

func (s *smartScanner) scan(data map[string]interface{}, norm *NormalizedCRMEvent) {
	if s.visitedDepth >= s.maxDepth {
		return
	}
	s.visitedDepth++
	defer func() { s.visitedDepth-- }()

	for key, val := range data {
		lowerKey := strings.ToLower(key)

		// Se for objeto aninhado, recursão
		if nestedMap, ok := val.(map[string]interface{}); ok {
			s.scan(nestedMap, norm)
			continue
		}

		// Se for slice de objetos
		if nestedSlice, ok := val.([]interface{}); ok {
			for _, item := range nestedSlice {
				if itemMap, ok := item.(map[string]interface{}); ok {
					s.scan(itemMap, norm)
				}
			}
			continue
		}

		// 1. Email matching
		if norm.Email == "" {
			if isEmailKey(lowerKey) {
				if strVal, ok := val.(string); ok && strings.Contains(strVal, "@") {
					norm.Email = strings.TrimSpace(strVal)
				}
			} else if strVal, ok := val.(string); ok && emailRegex.MatchString(strings.TrimSpace(strVal)) {
				norm.Email = strings.TrimSpace(strVal)
			}
		}

		// 2. Phone matching
		if norm.Phone == "" && isPhoneKey(lowerKey) {
			if strVal, ok := val.(string); ok && len(strings.TrimSpace(strVal)) >= 8 {
				norm.Phone = strings.TrimSpace(strVal)
			} else if numVal, ok := val.(float64); ok && numVal > 10000000 {
				norm.Phone = fmt.Sprintf("%.0f", numVal)
			}
		}

		// 3. Name matching
		if norm.Name == "" && isNameKey(lowerKey) {
			if strVal, ok := val.(string); ok && len(strVal) > 1 {
				norm.Name = strings.TrimSpace(strVal)
			}
		}

		// 4. Value matching
		if norm.Value == 0 && isValueKey(lowerKey) {
			norm.Value = toFloat64(val)
		}

		// 5. Currency matching
		if isCurrencyKey(lowerKey) {
			if strVal, ok := val.(string); ok && len(strVal) == 3 {
				norm.Currency = strings.ToUpper(strVal)
			}
		}

		// 6. Order / Deal ID matching
		if norm.OrderID == "" && isOrderIDKey(lowerKey) {
			if strVal, ok := val.(string); ok && strVal != "" {
				norm.OrderID = strVal
			} else if numVal, ok := val.(float64); ok && numVal > 0 {
				norm.OrderID = fmt.Sprintf("%.0f", numVal)
			}
		}

		// 7. Status matching
		if norm.Status == "" && isStatusKey(lowerKey) {
			if strVal, ok := val.(string); ok && strVal != "" {
				norm.Status = strVal
			}
		}

		// 8. Visitor ID matching
		if norm.VisitorID == "" && (strings.Contains(lowerKey, "visitor") || strings.Contains(lowerKey, "vis_id")) {
			if strVal, ok := val.(string); ok && strVal != "" {
				norm.VisitorID = strings.TrimSpace(strVal)
			}
		}
	}
}

func isEmailKey(k string) bool {
	return k == "email" || k == "mail" || k == "e-mail" || k == "email_address" ||
		k == "contact_email" || k == "cliente_email" || k == "lead_email" ||
		k == "user_email" || k == "email_principal" || strings.HasSuffix(k, "_email")
}

func isPhoneKey(k string) bool {
	return k == "phone" || k == "telefone" || k == "celular" || k == "mobile" ||
		k == "whatsapp" || k == "phone_number" || k == "contact_phone" ||
		k == "cliente_telefone" || k == "tel" || strings.HasSuffix(k, "_phone") ||
		strings.HasSuffix(k, "_telefone") || strings.HasSuffix(k, "_celular")
}

func isNameKey(k string) bool {
	return k == "name" || k == "nome" || k == "first_name" || k == "full_name" ||
		k == "razao_social" || k == "razaosocial" || k == "cliente_nome" ||
		k == "lead_nome" || k == "contato"
}

func isValueKey(k string) bool {
	return k == "value" || k == "valor" || k == "amount" || k == "total" ||
		k == "total_value" || k == "preco" || k == "price" || k == "deal_value" ||
		k == "venda_valor" || k == "total_pago" || k == "valortotal" ||
		k == "charge_amount" || k == "valorliquido"
}

func isCurrencyKey(k string) bool {
	return k == "currency" || k == "moeda" || k == "curr"
}

func isOrderIDKey(k string) bool {
	return k == "order_id" || k == "id" || k == "deal_id" || k == "pedido_id" ||
		k == "transacao_id" || k == "transaction_id" || k == "invoice_id" ||
		k == "numero_pedido" || k == "idpedido" || k == "cod_pedido"
}

func isStatusKey(k string) bool {
	return k == "status" || k == "dealstage" || k == "etapa" || k == "fase" ||
		k == "situacao" || k == "order_status" || k == "payment_status"
}
