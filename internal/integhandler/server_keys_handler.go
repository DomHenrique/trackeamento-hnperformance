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

// ServerKeyItem representa uma credencial de servidor para auditoria segura (sem segredo bruto)
type ServerKeyItem struct {
	ID                 string     `json:"id"`
	SiteID             string     `json:"site_id"`
	KeyPrefix          string     `json:"key_prefix"`
	Name               string     `json:"name"`
	Scopes             []string   `json:"scopes"`
	Status             string     `json:"status"` // 'active', 'inactive', 'revoked'
	CreatedBy          string     `json:"created_by"`
	RevokedBy          *string    `json:"revoked_by,omitempty"`
	RevokedAt          *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt         *time.Time `json:"last_used_at,omitempty"`
	TotalRequestsCount int64      `json:"total_requests_count"`
	CreatedAt          time.Time  `json:"created_at"`
}

// CreateServerKeyRequest payload para criação de nova chave de servidor
type CreateServerKeyRequest struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes,omitempty"`
}

// publishServerKeyInvalidation publica aviso de expurgo no canal Redis Pub/Sub
func (h *IntegrationsHandler) publishServerKeyInvalidation(ctx context.Context, keyID, siteID string) {
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
	_ = h.redis.Client.Publish(pubCtx, "pubsub:server_key_invalidation", string(payload)).Err()
}

// HandleListServerKeys lista todas as chaves de servidor de um site com exibição restrita ao prefixo mascarado
func (h *IntegrationsHandler) HandleListServerKeys(c *fiber.Ctx) error {
	siteIDStr := strings.TrimSpace(c.Params("site_id"))
	siteUUID, err := uuid.Parse(siteIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "site_id inválido"})
	}

	if h.pg == nil || h.pg.Pool == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "banco de dados indisponível"})
	}

	rows, err := h.pg.Pool.Query(c.Context(), `
		SELECT id::text, site_id::text, key_prefix, name, scopes, status, created_by, revoked_by, revoked_at, last_used_at, total_requests_count, created_at
		FROM server_api_keys
		WHERE site_id = $1
		ORDER BY created_at DESC
	`, siteUUID)
	if err != nil {
		log.Printf("[ServerKeys] Erro ao listar chaves de servidor do site %s: %v", siteUUID, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "falha ao listar chaves de servidor"})
	}
	defer rows.Close()

	keys := make([]ServerKeyItem, 0)

	for rows.Next() {
		var item ServerKeyItem
		var revokedBy *string
		var revokedAt *time.Time
		var lastUsedAt *time.Time

		err := rows.Scan(
			&item.ID, &item.SiteID, &item.KeyPrefix, &item.Name, &item.Scopes, &item.Status,
			&item.CreatedBy, &revokedBy, &revokedAt, &lastUsedAt,
			&item.TotalRequestsCount, &item.CreatedAt,
		)
		if err != nil {
			log.Printf("[ServerKeys] Erro no scan de chave de servidor do site %s: %v", siteUUID, err)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "falha ao processar chaves de servidor"})
		}

		item.RevokedBy = revokedBy
		item.RevokedAt = revokedAt
		item.LastUsedAt = lastUsedAt

		keys = append(keys, item)
	}

	if err := rows.Err(); err != nil {
		log.Printf("[ServerKeys] Erro ao iterar linhas de chaves de servidor do site %s: %v", siteUUID, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "falha ao iterar chaves de servidor"})
	}

	return c.Status(fiber.StatusOK).JSON(keys)
}

// HandleCreateServerKey gera nova chave de servidor hn_sec_, armazena hash SHA-256 e exibe o segredo uma única vez
func (h *IntegrationsHandler) HandleCreateServerKey(c *fiber.Ctx) error {
	siteIDStr := strings.TrimSpace(c.Params("site_id"))
	siteUUID, err := uuid.Parse(siteIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "site_id inválido"})
	}

	var req CreateServerKeyRequest
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
		name = "Chave de Servidor Inbound"
	}

	scopes := req.Scopes
	if len(scopes) == 0 {
		scopes = []string{"crm:inbound", "erp:conversions"}
	}

	createdBy := "operador"
	if u, ok := c.Locals("user").(string); ok && u != "" {
		createdBy = u
	} else if u, ok := c.Locals("username").(string); ok && u != "" {
		createdBy = u
	}

	if h.pg == nil || h.pg.Pool == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "banco de dados indisponível"})
	}

	// Cota de segurança: máximo 5 chaves de servidor ativas por site
	var activeCount int
	err = h.pg.Pool.QueryRow(c.Context(), `
		SELECT COUNT(*) FROM server_api_keys WHERE site_id = $1 AND status = 'active'
	`, siteUUID).Scan(&activeCount)
	if err != nil {
		log.Printf("[ServerKeys] Erro ao verificar cota de chaves para site %s: %v", siteUUID, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "falha ao verificar cota de chaves de servidor"})
	}
	if activeCount >= 5 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "limite máximo atingido (máximo de 5 chaves de servidor ativas por site)"})
	}

	// Geração da chave e cálculo de hash unidirecional
	rawKey := prefixedid.GenerateServerKey()
	keyHash := prefixedid.HashServerKey(rawKey)
	keyPrefix := prefixedid.MaskServerKey(rawKey)

	var newID string
	err = h.pg.Pool.QueryRow(c.Context(), `
		INSERT INTO server_api_keys (site_id, key_hash, key_prefix, name, scopes, status, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'active', $6, now(), now())
		RETURNING id::text
	`, siteUUID, keyHash, keyPrefix, name, scopes, createdBy).Scan(&newID)

	if err != nil {
		log.Printf("[ServerKeys] Erro ao persistir chave de servidor para site %s: %v", siteUUID, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "falha ao criar chave de servidor"})
	}

	// Resposta 201 Created: o segredo bruto rawKey é devolvido APENAS aqui
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"id":         newID,
		"site_id":    siteIDStr,
		"key":        rawKey,
		"key_prefix": keyPrefix,
		"name":       name,
		"scopes":     scopes,
		"status":     "active",
		"created_by": createdBy,
		"warning":    "Esta é a única vez em que a chave será exibida. Copie e armazene com segurança em seu cofre de credenciais.",
	})
}

// HandleRevokeServerKey revoga logicamente uma credencial de servidor via CTE atômico
func (h *IntegrationsHandler) HandleRevokeServerKey(c *fiber.Ctx) error {
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

	query := `
		WITH upd AS (
			UPDATE server_api_keys 
			SET status = 'revoked', revoked_by = $1, revoked_at = now(), updated_at = now()
			WHERE id = $2 AND site_id = $3 AND status <> 'revoked'
			RETURNING id::text, site_id::text, status, revoked_at, revoked_by
		)
		SELECT id, site_id, status, revoked_at, revoked_by, false AS already_revoked FROM upd
		UNION ALL
		SELECT id::text, site_id::text, status, revoked_at, revoked_by, true AS already_revoked FROM server_api_keys
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
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "chave de servidor não encontrada para este site"})
	}
	if err != nil {
		log.Printf("[ServerKeys] Erro ao revogar chave de servidor %s do site %s: %v", keyUUID, siteUUID, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "falha ao processar revogação da chave"})
	}

	// Publica no canal Redis de expurgo
	h.publishServerKeyInvalidation(c.Context(), id, sID)

	if alreadyRevoked {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{
			"error":      "chave de servidor já se encontra revogada",
			"id":         id,
			"site_id":    sID,
			"status":     "revoked",
			"revoked_at": revokedAt,
			"revoked_by": revBy,
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"message":    "Chave de servidor revogada com sucesso",
		"id":         id,
		"site_id":    sID,
		"status":     "revoked",
		"revoked_at": revokedAt,
		"revoked_by": revBy,
	})
}
