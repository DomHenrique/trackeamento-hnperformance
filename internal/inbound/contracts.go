package inbound

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	ErrFutureTimestamp = errors.New("data de ocorrência do evento não pode estar no futuro (tolerância máx 5 min)")
	reCurrency         = regexp.MustCompile(`^[A-Z]{3}$`)
)

// SanitizeCurrency normaliza e valida o código de moeda ISO 4217 (3 letras maiúsculas).
// Se vazio ou inválido, retorna "BRL".
func SanitizeCurrency(c string) string {
	clean := strings.ToUpper(strings.TrimSpace(c))
	if clean == "" || !reCurrency.MatchString(clean) {
		return "BRL"
	}
	return clean
}

// CurrencyExponent retorna o número de casas decimais para a moeda informada.
// BRL, USD, EUR usam 2; JPY, KRW usam 0; KWD, BHD usam 3.
func CurrencyExponent(currency string) int {
	switch strings.ToUpper(strings.TrimSpace(currency)) {
	case "JPY", "KRW", "CLP", "VND":
		return 0
	case "BHD", "KWD", "OMR":
		return 3
	default:
		return 2
	}
}

// ParseMoneyToCents converte um valor financeiro genérico (string, float64, int, json.Number)
// para centavos inteiros (int64) e código de moeda higienizado, com suporte resiliente a
// padrões brasileiros ("1.234,56") e internacionais ("1234.56").
func ParseMoneyToCents(raw interface{}, currencyInput string) (int64, string, error) {
	currency := SanitizeCurrency(currencyInput)
	exp := CurrencyExponent(currency)
	mult := math.Pow10(exp)

	if raw == nil {
		return 0, currency, nil
	}

	switch v := raw.(type) {
	case int:
		return int64(float64(v) * mult), currency, nil
	case int64:
		return int64(float64(v) * mult), currency, nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, currency, errors.New("valor numérico inválido (NaN ou Inf)")
		}
		return int64(math.Round(v * mult)), currency, nil
	case float32:
		return int64(math.Round(float64(v) * mult)), currency, nil
	case string:
		clean := strings.TrimSpace(v)
		if clean == "" {
			return 0, currency, nil
		}

		// Remove símbolos de moeda comuns e espaços (ex: "R$", "$", "€")
		clean = strings.TrimPrefix(clean, "R$")
		clean = strings.TrimPrefix(clean, "BRL")
		clean = strings.TrimPrefix(clean, "$")
		clean = strings.TrimPrefix(clean, "US$")
		clean = strings.TrimPrefix(clean, "€")
		clean = strings.TrimSpace(clean)

		hasComma := strings.Contains(clean, ",")
		hasDot := strings.Contains(clean, ".")

		if hasComma && hasDot {
			lastComma := strings.LastIndex(clean, ",")
			lastDot := strings.LastIndex(clean, ".")
			if lastComma > lastDot {
				// Formato brasileiro: 1.234,56 -> remove pontos de milhar e troca vírgula por ponto
				clean = strings.ReplaceAll(clean, ".", "")
				clean = strings.ReplaceAll(clean, ",", ".")
			} else {
				// Formato internacional: 1,234.56 -> remove vírgulas de milhar
				clean = strings.ReplaceAll(clean, ",", "")
			}
		} else if hasComma {
			// Apenas vírgula: 1234,56 ou 50,0 -> vírgula é o separador decimal
			clean = strings.ReplaceAll(clean, ",", ".")
		}

		parsedFloat, err := strconv.ParseFloat(clean, 64)
		if err != nil {
			return 0, currency, fmt.Errorf("impossível interpretar valor monetário '%s': %w", v, err)
		}
		return int64(math.Round(parsedFloat * mult)), currency, nil
	default:
		// Fallback string representation
		strVal := fmt.Sprintf("%v", raw)
		return ParseMoneyToCents(strVal, currency)
	}
}

// ComputeDedupKey calcula deterministicamente a chave de deduplicação (máx 128 chars).
// Ordem de precedência:
// 1. ProviderEventID (se houver)
// 2. sha256(order_id + ":" + event_name) (se order_id preenchido)
// 3. sha256(site_id + ":" + raw_body) (fallback para webhooks sem order_id)
func ComputeDedupKey(siteID string, norm *NormalizedCRMEvent, rawBody []byte) string {
	if norm != nil && strings.TrimSpace(norm.ProviderEventID) != "" {
		pID := strings.TrimSpace(norm.ProviderEventID)
		if len(pID) <= 128 {
			return pID
		}
		h := sha256.Sum256([]byte(pID))
		return hex.EncodeToString(h[:])
	}

	if norm != nil && strings.TrimSpace(norm.OrderID) != "" {
		orderID := strings.TrimSpace(norm.OrderID)
		evName := strings.TrimSpace(norm.EventName)
		if evName == "" {
			evName = "purchase"
		}
		raw := fmt.Sprintf("%s:%s", orderID, evName)
		h := sha256.Sum256([]byte(raw))
		return hex.EncodeToString(h[:])
	}

	// Fallback para webhooks sem order_id
	h := sha256.Sum256(append([]byte(siteID+":"), rawBody...))
	return hex.EncodeToString(h[:])
}

// ValidateOccurredAt valida o timestamp informado contra a janela temporal de conversões:
// - Se zero: assume now(), isStale=false
// - Se > now + 5 min: retorna erro ErrFutureTimestamp
// - Se < now - 7 dias (604.800s): retorna timestamp com isStale=true (para não despachar para Meta CAPI)
// - Caso contrário: retorna timestamp com isStale=false
func ValidateOccurredAt(t time.Time, now time.Time) (time.Time, bool, error) {
	if t.IsZero() {
		return now, false, nil
	}

	if t.After(now.Add(5 * time.Minute)) {
		return t, false, ErrFutureTimestamp
	}

	// 7 dias é o limite rígido da Meta Conversions API para eventos offline
	sevenDaysAgo := now.Add(-7 * 24 * time.Hour)
	if t.Before(sevenDaysAgo) {
		return t, true, nil
	}

	return t, false, nil
}

// DeterministicPurchaseEventID gera o identificador unificado de conversão para deduplicação
// estrita entre o evento de navegador do Meta Pixel e a Meta CAPI do servidor.
func DeterministicPurchaseEventID(orderID string) string {
	clean := strings.TrimSpace(orderID)
	if clean == "" {
		return ""
	}
	// Sanitiza caracteres especiais mantendo alfanuméricos, hífens e underscores
	reg := regexp.MustCompile(`[^a-zA-Z0-9_\-]`)
	sanitized := reg.ReplaceAllString(clean, "_")
	return "order_" + sanitized
}
