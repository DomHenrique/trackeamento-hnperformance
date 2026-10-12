package integhandler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"tracking-engine/internal/prefixedid"
)

// SiteKeyItem representa uma chave com telemetria e status
type SiteKeyItem struct {
	ID               string     `json:"id"`
	SiteID           string     `json:"site_id"`
	Key              string     `json:"key"`
	Name             string     `json:"name"`
	Status           string     `json:"status"`       // 'active', 'inactive', 'revoked'
	UsageStatus      string     `json:"usage_status"` // 'in_use', 'awaiting_traffic', 'revoked'
	CreatedBy        string     `json:"created_by"`
	RevokedBy        *string    `json:"revoked_by,omitempty"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt       *time.Time `json:"last_used_at,omitempty"`
	TotalEvents      int64      `json:"total_events"`
	TotalEventsCount int64      `json:"total_events_count"`
	EventsCount      int64      `json:"events_count"`
	CreatedAt        time.Time  `json:"created_at"`
}

// CreateKeyRequest payload para criação de nova chave
type CreateKeyRequest struct {
	Name string `json:"name"`
}

// publishKeyInvalidation publica aviso de invalidação no canal Redis Pub/Sub
func (h *IntegrationsHandler) publishKeyInvalidation(ctx context.Context, keyID, siteID string) {
	if h.redis == nil || h.redis.Client == nil {
		return
	}
	payload, err := json.Marshal(map[string]string{
		"action":  "revoke",
		"key_id":  keyID,
		"site_id": siteID,
	})
	if err != nil {
		return
	}
	pubCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_ = h.redis.Client.Publish(pubCtx, "pubsub:key_invalidation", string(payload)).Err()
}

// HandleListSiteKeys lista todas as chaves de um site com métricas de uso em modo fail-closed
func (h *IntegrationsHandler) HandleListSiteKeys(c *fiber.Ctx) error {
	siteIDStr := strings.TrimSpace(c.Params("site_id"))
	siteUUID, err := uuid.Parse(siteIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "site_id inválido"})
	}

	if h.pg == nil || h.pg.Pool == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "banco de dados indisponível"})
	}

	rows, err := h.pg.Pool.Query(c.Context(), `
		SELECT id::text, site_id::text, key, name, status, created_by, revoked_by, revoked_at, last_used_at, total_events_count, created_at
		FROM site_api_keys
		WHERE site_id = $1
		ORDER BY created_at DESC
	`, siteUUID)
	if err != nil {
		log.Printf("[SiteKeys] Erro ao listar chaves do site %s: %v", siteUUID, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "falha ao listar chaves do site"})
	}
	defer rows.Close()

	keys := make([]SiteKeyItem, 0)
	now := time.Now()

	for rows.Next() {
		var item SiteKeyItem
		var revokedBy *string
		var revokedAt *time.Time
		var lastUsedAt *time.Time

		err := rows.Scan(
			&item.ID, &item.SiteID, &item.Key, &item.Name, &item.Status,
			&item.CreatedBy, &revokedBy, &revokedAt, &lastUsedAt,
			&item.TotalEvents, &item.CreatedAt,
		)
		if err != nil {
			log.Printf("[SiteKeys] Erro no scan de chave do site %s: %v", siteUUID, err)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "falha ao processar dados das chaves de acesso"})
		}

		item.RevokedBy = revokedBy
		item.RevokedAt = revokedAt
		item.LastUsedAt = lastUsedAt
		item.TotalEventsCount = item.TotalEvents
		item.EventsCount = item.TotalEvents

		// Calcula status inteligente de uso
		if item.Status == "revoked" {
			item.UsageStatus = "revoked"
		} else if lastUsedAt != nil && now.Sub(*lastUsedAt) < 7*24*time.Hour {
			item.UsageStatus = "in_use"
		} else if lastUsedAt != nil {
			item.UsageStatus = "inactive_traffic"
		} else {
			item.UsageStatus = "awaiting_traffic"
		}

		keys = append(keys, item)
	}

	if err := rows.Err(); err != nil {
		log.Printf("[SiteKeys] Erro ao iterar linhas de chaves do site %s: %v", siteUUID, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "falha ao iterar chaves do site"})
	}

	return c.Status(fiber.StatusOK).JSON(keys)
}

// HandleCreateSiteKey gera uma nova chave para o site com validação estrita, quota e auditoria
func (h *IntegrationsHandler) HandleCreateSiteKey(c *fiber.Ctx) error {
	siteIDStr := strings.TrimSpace(c.Params("site_id"))
	siteUUID, err := uuid.Parse(siteIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "site_id inválido"})
	}

	var req CreateKeyRequest
	if len(c.Body()) > 0 {
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "payload JSON inválido"})
		}
	}

	name := strings.TrimSpace(req.Name)
	if len(name) > 100 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "o nome da chave não pode exceder 100 caracteres"})
	}
	name = strings.ReplaceAll(name, "<", "")
	name = strings.ReplaceAll(name, ">", "")
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Nova Chave de Acesso"
	}

	// Obtém usuário da sessão (se disponível)
	createdBy := "operador"
	if u, ok := c.Locals("user").(string); ok && u != "" {
		createdBy = u
	} else if u, ok := c.Locals("username").(string); ok && u != "" {
		createdBy = u
	}

	if h.pg == nil || h.pg.Pool == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "banco de dados indisponível"})
	}

	// Validação de cota de segurança: máximo 10 chaves ativas por site
	var activeCount int
	err = h.pg.Pool.QueryRow(c.Context(), `
		SELECT COUNT(*) FROM site_api_keys WHERE site_id = $1 AND status = 'active'
	`, siteUUID).Scan(&activeCount)
	if err != nil {
		log.Printf("[SiteKeys] Erro ao verificar contagem de chaves ativas para site %s: %v", siteUUID, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "falha ao verificar cota de chaves"})
	}
	if activeCount >= 10 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "limite máximo atingido (máximo de 10 chaves ativas por site)"})
	}

	newKey := prefixedid.GenerateSiteKey()
	var newID string

	err = h.pg.Pool.QueryRow(c.Context(), `
		INSERT INTO site_api_keys (site_id, key, name, status, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, 'active', $4, now(), now())
		RETURNING id::text
	`, siteUUID, newKey, name, createdBy).Scan(&newID)

	if err != nil {
		log.Printf("[SiteKeys] Erro ao criar chave para site %s: %v", siteUUID, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "falha ao criar chave de acesso"})
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"id":         newID,
		"site_id":    siteIDStr,
		"key":        newKey,
		"name":       name,
		"status":     "active",
		"created_by": createdBy,
	})
}

// HandleRevokeSiteKey revoga logicamente uma chave existente através de CTE atômico
func (h *IntegrationsHandler) HandleRevokeSiteKey(c *fiber.Ctx) error {
	siteIDStr := strings.TrimSpace(c.Params("site_id"))
	keyIDStr := strings.TrimSpace(c.Params("key_id"))

	siteUUID, err1 := uuid.Parse(siteIDStr)
	keyUUID, err2 := uuid.Parse(keyIDStr)
	if err1 != nil || err2 != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "parâmetros de UUID inválidos"})
	}

	revokedBy := "operador"
	if u, ok := c.Locals("user").(string); ok && u != "" {
		revokedBy = u
	} else if u, ok := c.Locals("username").(string); ok && u != "" {
		revokedBy = u
	}

	if h.pg == nil || h.pg.Pool == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "banco de dados indisponível"})
	}

	// CTE atômico: tenta o UPDATE se não estiver revogada.
	// Se já estava revogada, a cláusula UNION ALL recupera o registro com already_revoked = true sem sobrescrever auditoria.
	query := `
		WITH upd AS (
			UPDATE site_api_keys 
			SET status = 'revoked', revoked_by = $1, revoked_at = now(), updated_at = now()
			WHERE id = $2 AND site_id = $3 AND status <> 'revoked'
			RETURNING id::text, site_id::text, status, revoked_at, revoked_by
		)
		SELECT id, site_id, status, revoked_at, revoked_by, false AS already_revoked FROM upd
		UNION ALL
		SELECT id::text, site_id::text, status, revoked_at, revoked_by, true AS already_revoked FROM site_api_keys
		WHERE id = $2 AND site_id = $3 AND NOT EXISTS (SELECT 1 FROM upd);
	`

	var id, sID, status string
	var revokedAt *time.Time
	var revBy string
	var alreadyRevoked bool

	err := h.pg.Pool.QueryRow(c.Context(), query, revokedBy, keyUUID, siteUUID).Scan(
		&id, &sID, &status, &revokedAt, &revBy, &alreadyRevoked,
	)

	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, sql.ErrNoRows) {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "chave de acesso não encontrada para este site"})
	}
	if err != nil {
		log.Printf("[SiteKeys] Erro ao revogar chave %s do site %s: %v", keyUUID, siteUUID, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "falha ao processar revogação da chave"})
	}

	// Publica invalidação no canal Redis Pub/Sub com payload sanitizado (apenas key_id e site_id)
	h.publishKeyInvalidation(c.Context(), id, sID)

	// Callback em memória legado (se configurado)
	if h.onKeyRevoked != nil {
		h.onKeyRevoked(id)
	}

	if alreadyRevoked {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{
			"error":      "chave de acesso já se encontra revogada",
			"id":         id,
			"site_id":    sID,
			"status":     "revoked",
			"revoked_at": revokedAt,
			"revoked_by": revBy,
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"message":    "Chave revogada com sucesso",
		"id":         id,
		"site_id":    sID,
		"status":     "revoked",
		"revoked_at": revokedAt,
		"revoked_by": revBy,
	})
}
