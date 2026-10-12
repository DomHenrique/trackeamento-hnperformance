package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"tracking-engine/internal/prefixedid"
)

var (
	ErrServerKeyMissing     = errors.New("chave de servidor ausente")
	ErrPublicKeyForbidden   = errors.New("chaves client-side de site são estritamente proibidas para inbound")
	ErrServerKeyInvalid     = errors.New("chave de servidor inválida ou inativa")
	ErrServerKeyScopeDenied = errors.New("escopo de permissão insuficiente")
)

// ServerKeyMetadata armazena os metadados de uma chave de servidor autenticada em cache
type ServerKeyMetadata struct {
	ID        string    `json:"id"`
	SiteID    string    `json:"site_id"`
	Name      string    `json:"name"`
	Scopes    []string  `json:"scopes"`
	ExpiresAt time.Time `json:"expires_at"`
}

// AuthenticateServerRequest inspeciona a requisição HTTP e autentica a credencial de servidor hn_sec_
func (h *Handler) AuthenticateServerRequest(c *fiber.Ctx) (*ServerKeyMetadata, error) {
	// 1. Detecção defensiva de chaves client-side (bloqueio estrito anti-spoofing)
	if pubKey := c.Get("X-Site-Key"); pubKey != "" || strings.HasPrefix(c.Query("site_key"), prefixedid.PrefixSite) {
		return nil, ErrPublicKeyForbidden
	}
	if pubKey := c.Get("X-HN-Site-Key"); pubKey != "" {
		return nil, ErrPublicKeyForbidden
	}

	// 2. Extração da credencial de servidor
	rawKey := strings.TrimSpace(c.Get("X-Server-Key"))
	if rawKey == "" {
		authHdr := strings.TrimSpace(c.Get("Authorization"))
		if strings.HasPrefix(strings.ToLower(authHdr), "bearer ") {
			rawKey = strings.TrimSpace(authHdr[7:])
		}
	}
	if rawKey == "" {
		rawKey = strings.TrimSpace(c.Query("server_key"))
	}

	// Se tentou passar site_key via query ou body
	if rawKey == "" && c.Query("site_key") != "" {
		sk := c.Query("site_key")
		if strings.HasPrefix(sk, prefixedid.PrefixSite) || strings.HasPrefix(sk, prefixedid.PrefixLegacySite) {
			return nil, ErrPublicKeyForbidden
		}
	}

	if rawKey == "" {
		return nil, ErrServerKeyMissing
	}

	// Verifica se a chave informada é client-side
	if strings.HasPrefix(rawKey, prefixedid.PrefixSite) || strings.HasPrefix(rawKey, prefixedid.PrefixLegacySite) {
		return nil, ErrPublicKeyForbidden
	}

	// Validação de prefixo esperado hn_sec_
	if !strings.HasPrefix(rawKey, prefixedid.PrefixServer) {
		return nil, ErrServerKeyInvalid
	}

	keyHash := prefixedid.HashServerKey(rawKey)
	if keyHash == "" {
		return nil, ErrServerKeyInvalid
	}

	// 3. Consulta no Cache em Memória com TTL de 5 minutos
	if val, ok := h.serverKeys.Load(keyHash); ok {
		if meta, ok := val.(*ServerKeyMetadata); ok {
			if time.Now().Before(meta.ExpiresAt) {
				return meta, nil
			}
			h.serverKeys.Delete(keyHash)
		}
	}

	// 4. Consulta no PostgreSQL
	if h.pg == nil || h.pg.Pool == nil {
		return nil, errors.New("banco de dados indisponível para autenticação de servidor")
	}

	var meta ServerKeyMetadata
	var isActive bool
	err := h.pg.Pool.QueryRow(c.Context(), `
		SELECT id::text, site_id::text, name, scopes, (status = 'active')
		FROM server_api_keys
		WHERE key_hash = $1
	`, keyHash).Scan(&meta.ID, &meta.SiteID, &meta.Name, &meta.Scopes, &isActive)

	if err != nil || !isActive {
		return nil, ErrServerKeyInvalid
	}

	// Salva no cache com TTL de 5 minutos
	meta.ExpiresAt = time.Now().Add(5 * time.Minute)
	h.serverKeys.Store(keyHash, &meta)

	// Atualiza telemetria de uso assincronamente sem bloquear a requisição
	go func(kh string) {
		upCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = h.pg.Pool.Exec(upCtx, `
			UPDATE server_api_keys
			SET last_used_at = now(), total_requests_count = total_requests_count + 1
			WHERE key_hash = $1
		`, kh)
	}(keyHash)

	return &meta, nil
}

// RequireServerKeyAuth middleware para proteger rotas de inbound de CRM/ERP
func (h *Handler) RequireServerKeyAuth(requiredScope ...string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		meta, err := h.AuthenticateServerRequest(c)
		if err != nil {
			if errors.Is(err, ErrPublicKeyForbidden) {
				return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
					"error": "chaves client-side (hn_site_) são estritamente proibidas para inbound de ERP/CRM; utilize uma chave de servidor (hn_sec_)",
				})
			}
			if errors.Is(err, ErrServerKeyMissing) {
				return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
					"error": "autenticação de servidor ausente. Forneça o cabeçalho X-Server-Key ou Authorization: Bearer hn_sec_...",
				})
			}
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "chave de servidor inválida ou inativa",
			})
		}

		// Validação anti-IDOR se houver :site_id na URL
		if siteIDParam := strings.TrimSpace(c.Params("site_id")); siteIDParam != "" {
			if !strings.EqualFold(siteIDParam, meta.SiteID) {
				return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
					"error": "acesso negado: a chave de servidor informada não pertence ao site especificado na URL",
				})
			}
		}

		// Validação de escopo necessário
		if len(requiredScope) > 0 && requiredScope[0] != "" {
			scopeReq := requiredScope[0]
			hasScope := false
			for _, s := range meta.Scopes {
				if s == scopeReq || s == "*" || s == "admin" {
					hasScope = true
					break
				}
			}
			if !hasScope {
				return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
					"error": fmt.Sprintf("a chave de servidor não possui o escopo necessário (%s)", scopeReq),
				})
			}
		}

		c.Locals("server_auth", meta)
		c.Locals("authenticated_site_id", meta.SiteID)
		return c.Next()
	}
}

// InvalidateServerKeyByID purga a chave de servidor do cache em memória pelo ID
func (h *Handler) InvalidateServerKeyByID(keyID string) {
	if keyID == "" {
		return
	}
	h.serverKeys.Range(func(key, val interface{}) bool {
		if meta, ok := val.(*ServerKeyMetadata); ok && meta.ID == keyID {
			h.serverKeys.Delete(key)
		}
		return true
	})
}

// InvalidateServerKeysBySite purga todas as chaves de servidor do site do cache em memória
func (h *Handler) InvalidateServerKeysBySite(siteID string) {
	if siteID == "" {
		return
	}
	h.serverKeys.Range(func(key, val interface{}) bool {
		if meta, ok := val.(*ServerKeyMetadata); ok && meta.SiteID == siteID {
			h.serverKeys.Delete(key)
		}
		return true
	})
}

// ClearServerKeysCache limpa completamente o cache de chaves de servidor
func (h *Handler) ClearServerKeysCache() {
	h.serverKeys.Range(func(key, val interface{}) bool {
		h.serverKeys.Delete(key)
		return true
	})
}

// StartServerKeyInvalidationSubscriber escuta o canal pubsub:server_key_invalidation e purga chaves revogadas
func (h *Handler) StartServerKeyInvalidationSubscriber(ctx context.Context) {
	if h.redis == nil || h.redis.Client == nil {
		return
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			pubsub := h.redis.Client.Subscribe(ctx, "pubsub:server_key_invalidation")
			h.ClearServerKeysCache()

			ch := pubsub.Channel()
			for msg := range ch {
				var payload struct {
					Action string `json:"action"`
					KeyID  string `json:"key_id"`
					SiteID string `json:"site_id"`
				}
				if err := json.Unmarshal([]byte(msg.Payload), &payload); err == nil {
					if payload.KeyID != "" {
						h.InvalidateServerKeyByID(payload.KeyID)
					}
					if payload.SiteID != "" {
						h.InvalidateServerKeysBySite(payload.SiteID)
					}
				}
			}

			_ = pubsub.Close()
			h.ClearServerKeysCache()

			select {
			case <-ctx.Done():
				return
			case <-time.After(1 * time.Second):
			}
		}
	}()
}
