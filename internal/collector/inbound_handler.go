package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"tracking-engine/internal/inbound"
)

var (
	reValidProvider = regexp.MustCompile(`^[a-z0-9_\-]{1,32}$`)
)

// InboundPersistResult encapsula o resultado da persistência de um evento
type InboundPersistResult struct {
	InboundID   string
	IsDuplicate bool
}

// InboundPersisterFunc define a assinatura para persistência transacional de eventos
type InboundPersisterFunc func(ctx context.Context, siteID, provider, eventName, status, email, phone string, val float64, valCents int64, cur, orderID, dedupKey string, occurredAt time.Time, rawPayload map[string]interface{}) (*InboundPersistResult, error)

// OfflineReconcileFunc define a função injetada para orquestrar a reconciliação reversa sem acoplamento circular
type OfflineReconcileFunc func(ctx context.Context, siteID string, norm *inbound.NormalizedCRMEvent, inboundEventID string) error

// SetOfflineReconciler configura a função de reconciliação de identidade e atualiza o worker
func (h *Handler) SetOfflineReconciler(fn OfflineReconcileFunc) {
	h.offlineReconciler = fn
	if h.inboundWorker != nil {
		h.inboundWorker.reconciler = fn
	}
}

// SetInboundPersister injeta uma função customizada de persistência (útil para testes unitários isolados)
func (h *Handler) SetInboundPersister(fn InboundPersisterFunc) {
	h.inboundPersister = fn
}

// HandleInboundCRMWebhook recebe e processa webhooks de CRMs, ERPs e gateways de pagamento
func (h *Handler) HandleInboundCRMWebhook(c *fiber.Ctx) error {
	// Proteção contra payloads abusivos (> 256 KB)
	if len(c.Body()) > 256*1024 {
		return c.Status(fiber.StatusRequestEntityTooLarge).JSON(fiber.Map{
			"error": "payload do webhook excede o limite máximo permitido de 256 KB",
		})
	}

	rawProvider := strings.ToLower(strings.TrimSpace(c.Params("provider")))
	if rawProvider == "" {
		rawProvider = "custom"
	}

	if !reValidProvider.MatchString(rawProvider) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "identificador de provedor inválido na rota (deve conter apenas caracteres alfanuméricos, hífen ou underscore até 32 caracteres)",
		})
	}
	provider := rawProvider

	// 1. Resolução e validação estrita da credencial de servidor (hn_sec_)
	var siteID string
	if authVal, ok := c.Locals("server_auth").(*ServerKeyMetadata); ok && authVal != nil {
		siteID = authVal.SiteID
	} else {
		serverMeta, errAuth := h.AuthenticateServerRequest(c)
		if errAuth != nil {
			if errors.Is(errAuth, ErrPublicKeyForbidden) {
				return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
					"error": "chaves client-side (hn_site_) são estritamente proibidas para inbound de ERP/CRM; utilize uma credencial de servidor (hn_sec_)",
				})
			}
			if errors.Is(errAuth, ErrServerKeyMissing) {
				return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
					"error": "autenticação de servidor ausente. Forneça o cabeçalho X-Server-Key ou Authorization: Bearer hn_sec_...",
				})
			}
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "chave de servidor inválida ou inativa",
			})
		}
		siteID = serverMeta.SiteID
	}

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

	// 3. Normalização de Valor Monetário em Centavos (int64) e Moeda ISO 4217
	valInput := interface{}(norm.Value)
	if norm.ValueCents > 0 {
		valInput = norm.ValueCents
	}
	cents, sanitizedCurrency, errMoney := inbound.ParseMoneyToCents(valInput, norm.Currency)
	if errMoney != nil {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{
			"error":    fmt.Sprintf("valor monetário inválido: %v", errMoney),
			"provider": provider,
		})
	}
	norm.ValueCents = cents
	norm.Currency = sanitizedCurrency
	norm.Value = float64(cents) / 100.0

	// 4. Validação Temporal de Ocorrência da Conversão (Janela Meta 7 dias)
	occurredAt, isStale, errOccurred := inbound.ValidateOccurredAt(norm.OccurredAt, time.Now())
	if errOccurred != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": errOccurred.Error(),
		})
	}
	norm.OccurredAt = occurredAt

	initialStatus := "pending"
	if isStale {
		initialStatus = "stale"
	}

	// 5. Cálculo Determinístico da Chave de Deduplicação
	dedupKey := inbound.ComputeDedupKey(siteID, norm, body)

	// 6. Persistência Transacional Atômica com ON CONFLICT (site_id, dedup_key) DO NOTHING
	var inboundEventID string
	if h.inboundPersister != nil {
		res, errPersist := h.inboundPersister(c.Context(), siteID, provider, norm.EventName, initialStatus,
			norm.Email, norm.Phone, norm.Value, norm.ValueCents, norm.Currency,
			norm.OrderID, dedupKey, norm.OccurredAt, norm.RawPayload)
		if errPersist != nil {
			log.Printf("[InboundCRM] Erro no persister injetado: %v", errPersist)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "falha na persistência do evento de inbound",
			})
		}
		if res != nil && res.IsDuplicate {
			return c.Status(fiber.StatusOK).JSON(fiber.Map{
				"status":     "duplicate_ignored",
				"provider":   provider,
				"order_id":   norm.OrderID,
				"inbound_id": res.InboundID,
				"message":    "evento já persistido anteriormente dentro da janela de idempotência",
			})
		}
		if res != nil {
			inboundEventID = res.InboundID
		}
	} else {
		if h.pg == nil || h.pg.Pool == nil {
			log.Printf("[InboundCRM] Falha crítica: pool de conexões do PostgreSQL indisponível")
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"error": "serviço de persistência temporariamente indisponível para inbound",
			})
		}

		siteUUID, errUUID := uuid.Parse(siteID)
		if errUUID != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "site_id inválido",
			})
		}

		payloadBytes, _ := json.Marshal(norm.RawPayload)
		var insertedUUID uuid.UUID

		errInsert := h.pg.Pool.QueryRow(c.Context(), `
			INSERT INTO crm_inbound_events (
				site_id, provider, event_name, status,
				contact_email, contact_phone, value, value_cents, currency,
				order_id, dedup_key, occurred_at, payload
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			ON CONFLICT (site_id, dedup_key) DO NOTHING
			RETURNING id
		`, siteUUID, provider, norm.EventName, initialStatus,
			norm.Email, norm.Phone, norm.Value, norm.ValueCents, norm.Currency,
			norm.OrderID, dedupKey, norm.OccurredAt, payloadBytes).Scan(&insertedUUID)

		if errInsert != nil {
			// Se não retornou linhas pelo ON CONFLICT DO NOTHING (pgx.ErrNoRows)
			if strings.Contains(errInsert.Error(), "no rows in result set") {
				var existingID string
				_ = h.pg.Pool.QueryRow(c.Context(), `
					SELECT id::text
					FROM crm_inbound_events
					WHERE site_id = $1 AND dedup_key = $2
				`, siteUUID, dedupKey).Scan(&existingID)

				log.Printf("[InboundCRM] Webhook duplicado idempotente ignorado: site=%s provider=%s dedup_key=%s id=%s",
					siteID, provider, dedupKey, existingID)

				return c.Status(fiber.StatusOK).JSON(fiber.Map{
					"status":     "duplicate_ignored",
					"provider":   provider,
					"order_id":   norm.OrderID,
					"inbound_id": existingID,
					"message":    "evento já persistido anteriormente dentro da janela de idempotência",
				})
			}

			// Erro real de escrita no banco -> Fail-Fast 500 para permitir retry do ERP
			log.Printf("[InboundCRM] Erro na inserção no PostgreSQL: %v", errInsert)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "falha na persistência do evento de inbound",
			})
		}

		inboundEventID = insertedUUID.String()
	}

	// 7. Notificação de Baixa Latência para o Worker Durável (sem goroutines soltas na requisição)
	if initialStatus == "pending" {
		if h.inboundWorker != nil {
			h.inboundWorker.Notify()
		}
		if h.redis != nil && h.redis.Client != nil {
			_ = h.redis.Client.Publish(c.Context(), InboundEventReadyChannel, inboundEventID).Err()
		}
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
		       value, value_cents, currency,
		       COALESCE(order_id, ''),
		       COALESCE(dedup_key, ''),
		       payload,
		       COALESCE(error_message, ''),
		       attempts, occurred_at,
		       created_at, processed_at
		FROM crm_inbound_events
		WHERE site_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, siteUUID, limit)
	if err != nil {
		log.Printf("[InboundCRM] Erro ao listar eventos de inbound: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "falha ao recuperar eventos de inbound",
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
		ValueCents       int64                  `json:"value_cents"`
		Currency         string                 `json:"currency"`
		OrderID          string                 `json:"order_id"`
		DedupKey         string                 `json:"dedup_key"`
		Payload          map[string]interface{} `json:"payload"`
		ErrorMessage     string                 `json:"error_message,omitempty"`
		Attempts         int                    `json:"attempts"`
		OccurredAt       time.Time              `json:"occurred_at"`
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
			&rec.Value, &rec.ValueCents, &rec.Currency, &rec.OrderID,
			&rec.DedupKey,
			&rawPayload, &rec.ErrorMessage,
			&rec.Attempts, &rec.OccurredAt,
			&rec.CreatedAt, &rec.ProcessedAt,
		); errScan != nil {
			log.Printf("[InboundCRM] Erro no scan de evento de inbound: %v", errScan)
			continue
		}

		rec.ID = idUUID.String()
		rec.SiteID = sUUID.String()
		if len(rawPayload) > 0 {
			_ = json.Unmarshal(rawPayload, &rec.Payload)
		}
		events = append(events, rec)
	}

	if errRows := rows.Err(); errRows != nil {
		log.Printf("[InboundCRM] Erro durante iteração de rows: %v", errRows)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "erro ao iterar registros de inbound",
		})
	}

	return c.JSON(fiber.Map{
		"site_id": siteID,
		"events":  events,
		"total":   len(events),
	})
}
