package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"tracking-engine/internal/inbound"
	"tracking-engine/internal/prefixedid"
)

// OfflineReconcileFunc define a função injetada para orquestrar a reconciliação reversa sem acoplamento circular
type OfflineReconcileFunc func(ctx context.Context, siteID string, norm *inbound.NormalizedCRMEvent, inboundEventID string) error

// SetOfflineReconciler configura a função de reconciliação de identidade
func (h *Handler) SetOfflineReconciler(fn OfflineReconcileFunc) {
	h.offlineReconciler = fn
}

// HandleInboundCRMWebhook recebe e processa webhooks de CRMs, ERPs e gateways de pagamento
func (h *Handler) HandleInboundCRMWebhook(c *fiber.Ctx) error {
	provider := strings.ToLower(strings.TrimSpace(c.Params("provider")))
	if provider == "" {
		if strings.Contains(c.Path(), "/custom") {
			provider = "custom"
		} else {
			provider = "custom"
		}
	}

	// 1. Resolução e validação da chave do site (site_key)
	siteKey := prefixedid.SanitizeKey(c.Query("site_key"))
	if siteKey == "" {
		siteKey = prefixedid.SanitizeKey(c.Get("X-Site-Key"))
	}
	if siteKey == "" {
		siteKey = prefixedid.SanitizeKey(c.Get("X-HN-Site-Key"))
	}
	if siteKey == "" {
		authHdr := c.Get("Authorization")
		if strings.HasPrefix(strings.ToLower(authHdr), "bearer ") {
			siteKey = prefixedid.SanitizeKey(authHdr[7:])
		}
	}

	if siteKey == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "site_key ausente. Forneça ?site_key=hn_site_... na URL ou cabeçalho X-Site-Key",
		})
	}

	siteMeta, valid := h.validateSiteKey(c.Context(), siteKey)
	if !valid || siteMeta == nil || siteMeta.ID == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "site_key inválida ou inativa",
		})
	}
	siteID := siteMeta.ID

	// 2. Parser do Adapter específico ou Smart Auto-Mapping
	body := c.Body()
	adapter := inbound.GetAdapter(provider)
	norm, err := adapter.Parse(c, body)
	if err != nil {
		if errors.Is(err, inbound.ErrNoContactIdentifier) {
			return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{
				"error":    "nenhum identificador de contato (email ou telefone) encontrado no payload para reconciliação",
				"provider": provider,
			})
		}
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error":    fmt.Sprintf("falha ao interpretar payload de %s: %v", provider, err),
			"provider": provider,
		})
	}

	// 3. Deduplicação Idempotente via Redis (Janela de 7 dias)
	if norm.OrderID != "" && h.redis != nil && h.redis.Client != nil {
		dedupKey := fmt.Sprintf("crm:dedup:%s:%s:%s", siteID, provider, norm.OrderID)
		isNew, errRedis := h.redis.Client.SetNX(c.Context(), dedupKey, "1", 7*24*time.Hour).Result()
		if errRedis == nil && !isNew {
			log.Printf("[InboundCRM] Webhook duplicado ignorado: site=%s provider=%s order_id=%s", siteID, provider, norm.OrderID)
			return c.Status(fiber.StatusOK).JSON(fiber.Map{
				"status":   "duplicate_ignored",
				"provider": provider,
				"order_id": norm.OrderID,
				"message":  "evento já processado anteriormente dentro da janela de idempotência",
			})
		}
	}

	// 4. Registro de auditoria no PostgreSQL
	inboundEventID := ""
	if h.pg != nil && h.pg.Pool != nil {
		siteUUID, errUUID := uuid.Parse(siteID)
		if errUUID == nil {
			payloadBytes, _ := json.Marshal(norm.RawPayload)
			var insertedUUID uuid.UUID
			errInsert := h.pg.Pool.QueryRow(c.Context(), `
				INSERT INTO crm_inbound_events (
					site_id, provider, event_name, status,
					contact_email, contact_phone, value, currency, order_id, payload
				) VALUES ($1, $2, $3, 'pending', $4, $5, $6, $7, $8, $9)
				RETURNING id
			`, siteUUID, provider, norm.EventName, norm.Email, norm.Phone, norm.Value, norm.Currency, norm.OrderID, payloadBytes).Scan(&insertedUUID)

			if errInsert == nil {
				inboundEventID = insertedUUID.String()
			}
		}
	}

	// 5. Execução da Reconciliação Reversa em background para manter latência < 15ms
	if h.offlineReconciler != nil {
		go func(sID, inID string, n *inbound.NormalizedCRMEvent) {
			bgCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if errRec := h.offlineReconciler(bgCtx, sID, n, inID); errRec != nil {
				log.Printf("[InboundCRM] Erro na reconciliação offline (%s / %s): %v", sID, inID, errRec)
			}
		}(siteID, inboundEventID, norm)
	}

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
		"status":     "accepted",
		"provider":   provider,
		"event_name": norm.EventName,
		"order_id":   norm.OrderID,
		"inbound_id": inboundEventID,
	})
}

// HandleListInboundEvents retorna os eventos recentes de inbound CRM para o Live Webhook Inspector no Dashboard
func (h *Handler) HandleListInboundEvents(c *fiber.Ctx) error {
	siteID := strings.TrimSpace(c.Params("site_id"))
	if siteID == "" {
		siteID = strings.TrimSpace(c.Query("site_id"))
	}

	siteUUID, err := uuid.Parse(siteID)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "site_id inválido",
		})
	}

	limit := 50
	if l := c.QueryInt("limit"); l > 0 && l <= 100 {
		limit = l
	}

	if h.pg == nil || h.pg.Pool == nil {
		return c.JSON(fiber.Map{
			"site_id": siteID,
			"events":  []interface{}{},
			"total":   0,
		})
	}

	rows, err := h.pg.Pool.Query(c.Context(), `
		SELECT id, site_id, provider, event_name, status,
		       COALESCE(matched_visitor_id, ''),
		       COALESCE(contact_email, ''),
		       COALESCE(contact_phone, ''),
		       value, currency,
		       COALESCE(order_id, ''),
		       payload,
		       COALESCE(error_message, ''),
		       created_at, processed_at
		FROM crm_inbound_events
		WHERE site_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, siteUUID, limit)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}
	defer rows.Close()

	type InboundEventRecord struct {
		ID               string                 `json:"id"`
		SiteID           string                 `json:"site_id"`
		Provider         string                 `json:"provider"`
		EventName        string                 `json:"event_name"`
		Status           string                 `json:"status"`
		MatchedVisitorID string                 `json:"matched_visitor_id"`
		ContactEmail     string                 `json:"contact_email"`
		ContactPhone     string                 `json:"contact_phone"`
		Value            float64                `json:"value"`
		Currency         string                 `json:"currency"`
		OrderID          string                 `json:"order_id"`
		Payload          map[string]interface{} `json:"payload"`
		ErrorMessage     string                 `json:"error_message,omitempty"`
		CreatedAt        time.Time              `json:"created_at"`
		ProcessedAt      *time.Time             `json:"processed_at,omitempty"`
	}

	events := make([]InboundEventRecord, 0)
	for rows.Next() {
		var rec InboundEventRecord
		var rawPayload []byte
		var idUUID, sUUID uuid.UUID
		if errScan := rows.Scan(
			&idUUID, &sUUID, &rec.Provider, &rec.EventName, &rec.Status,
			&rec.MatchedVisitorID, &rec.ContactEmail, &rec.ContactPhone,
			&rec.Value, &rec.Currency, &rec.OrderID,
			&rawPayload, &rec.ErrorMessage,
			&rec.CreatedAt, &rec.ProcessedAt,
		); errScan == nil {
			rec.ID = idUUID.String()
			rec.SiteID = sUUID.String()
			if len(rawPayload) > 0 {
				_ = json.Unmarshal(rawPayload, &rec.Payload)
			}
			events = append(events, rec)
		}
	}

	return c.JSON(fiber.Map{
		"site_id": siteID,
		"events":  events,
		"total":   len(events),
	})
}
