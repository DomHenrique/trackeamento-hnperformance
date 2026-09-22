package integhandler

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"tracking-engine/internal/prefixedid"
)

// SiteKeyItem representa uma chave com telemetria e status
type SiteKeyItem struct {
	ID          string     `json:"id"`
	SiteID      string     `json:"site_id"`
	Key         string     `json:"key"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`       // 'active', 'inactive', 'revoked'
	UsageStatus string     `json:"usage_status"` // 'in_use', 'awaiting_traffic', 'revoked'
	CreatedBy   string     `json:"created_by"`
	RevokedBy   *string    `json:"revoked_by,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	TotalEvents int64      `json:"total_events"`
	CreatedAt   time.Time  `json:"created_at"`
}

// CreateKeyRequest payload para criação de nova chave
type CreateKeyRequest struct {
	Name string `json:"name"`
}

// HandleListSiteKeys lista todas as chaves de um site com métricas de uso
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
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
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
		if err == nil {
			item.RevokedBy = revokedBy
			item.RevokedAt = revokedAt
			item.LastUsedAt = lastUsedAt

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
	}

	return c.Status(fiber.StatusOK).JSON(keys)
}

// HandleCreateSiteKey gera uma nova chave para o site com auditoria do usuário
func (h *IntegrationsHandler) HandleCreateSiteKey(c *fiber.Ctx) error {
	siteIDStr := strings.TrimSpace(c.Params("site_id"))
	siteUUID, err := uuid.Parse(siteIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "site_id inválido"})
	}

	var req CreateKeyRequest
	_ = c.BodyParser(&req)

	name := strings.TrimSpace(req.Name)
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

	newKey := prefixedid.GenerateSiteKey()
	var newID string

	err = h.pg.Pool.QueryRow(c.Context(), `
		INSERT INTO site_api_keys (site_id, key, name, status, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, 'active', $4, now(), now())
		RETURNING id::text
	`, siteUUID, newKey, name, createdBy).Scan(&newID)

	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "erro ao criar chave: " + err.Error()})
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

// HandleRevokeSiteKey revoga logicamente uma chave existente
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

	// Busca a chave para invalidar cache
	var rawKey string
	err := h.pg.Pool.QueryRow(c.Context(), `
		UPDATE site_api_keys 
		SET status = 'revoked', revoked_by = $1, revoked_at = now(), updated_at = now()
		WHERE id = $2 AND site_id = $3
		RETURNING key
	`, revokedBy, keyUUID, siteUUID).Scan(&rawKey)

	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "chave não encontrada ou já revogada"})
	}

	// Se houver callback de invalidação registrado, dispara
	if h.onKeyRevoked != nil {
		h.onKeyRevoked(rawKey)
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"message":    "Chave revogada com sucesso",
		"key":        rawKey,
		"revoked_by": revokedBy,
	})
}
