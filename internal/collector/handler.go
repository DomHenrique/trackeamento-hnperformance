package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"tracking-engine/internal/attribution"
	"tracking-engine/internal/config"
	"tracking-engine/internal/storage"
)

const (
	VisitorCookieName = "_vid"
	OneYearSeconds    = 365 * 24 * 60 * 60
)

type Handler struct {
	cfg      *config.Config
	redis    *storage.RedisClient
	pg       *storage.PostgresDB
	siteKeys sync.Map // Cache em memória para validação ultra-rápida de site_key
}

func NewHandler(cfg *config.Config, rdb *storage.RedisClient, pg *storage.PostgresDB) *Handler {
	return &Handler{
		cfg:   cfg,
		redis: rdb,
		pg:    pg,
	}
}

// HandleCollect recebe e processa a requisição de coleta em tempo sub-milissegundo
func (h *Handler) HandleCollect(c *fiber.Ctx) error {
	var req EventRequest
	if err := c.BodyParser(&req); err != nil {
		// Fallback para requisições vazias ou mal formatadas
		req = EventRequest{}
	}

	siteKey := strings.TrimSpace(req.SiteKey)
	if siteKey == "" {
		siteKey = strings.TrimSpace(c.Query("site_key"))
	}
	if siteKey == "" {
		siteKey = strings.TrimSpace(c.Get("X-Site-Key"))
	}

	if siteKey == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "site_key ausente",
		})
	}

	// Validação da site_key com cache
	siteID, valid := h.validateSiteKey(c.Context(), siteKey)
	if !valid {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "site_key invalida ou inativa",
		})
	}

	// 1. Gerenciamento do Cookie 1st-Party _vid
	visitorID := c.Cookies(VisitorCookieName)
	if visitorID == "" {
		if v, ok := req.UserData["visitor_id"].(string); ok && v != "" {
			visitorID = v
		}
	}

	isNewVisitor := false
	if visitorID == "" {
		visitorID = "v_" + strings.ReplaceAll(uuid.New().String(), "-", "")
		isNewVisitor = true
	}

	// Renova/Emite o cookie com expiração de 1 ano
	c.Cookie(&fiber.Cookie{
		Name:     VisitorCookieName,
		Value:    visitorID,
		MaxAge:   OneYearSeconds,
		Path:     "/",
		SameSite: "Lax",
		Secure:   h.cfg.Env == "production",
		HTTPOnly: false, // Permite acesso pelo SDK JavaScript se necessário
	})

	// 2. Extração de IP Real
	ip := c.Get("CF-Connecting-IP")
	if ip == "" {
		ip = c.Get("X-Real-IP")
	}
	if ip == "" {
		ip = c.IP()
	}

	// 3. User-Agent e Tipo de Dispositivo
	ua := c.Get("User-Agent")
	deviceType := detectDeviceType(ua)

	// 4. Session ID
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = "s_" + strings.ReplaceAll(uuid.New().String(), "-", "")
	}

	// 5. Event ID para deduplicação (especialmente com Meta Pixel)
	eventID := req.EventID
	if eventID == "" {
		eventID = uuid.New().String()
	}

	// 6. URL e Referrer
	pageURL := req.URL
	if pageURL == "" {
		pageURL = req.PageURL
	}
	if pageURL == "" {
		pageURL = c.Get("Referer")
	}
	referrer := req.Referrer

	// 7. Extração de parâmetros de Atribuição
	attrParams := attribution.ParseURL(pageURL, referrer)

	// 8. Normalização do nome do evento
	eventName := strings.TrimSpace(req.EventName)
	if eventName == "" {
		eventName = "page_view"
	}

	payload := EventPayload{
		EventID:     eventID,
		SiteID:      siteID,
		SiteKey:     siteKey,
		VisitorID:   visitorID,
		SessionID:   sessionID,
		EventName:   eventName,
		EventTime:   time.Now().UTC(),
		IPAddress:   ip,
		UserAgent:   ua,
		DeviceType:  deviceType,
		Attribution: attrParams,
		UserData:    req.UserData,
		CustomData:  req.CustomData,
		CreatedAt:   time.Now().UTC(),
	}

	if isNewVisitor {
		if payload.UserData == nil {
			payload.UserData = make(map[string]interface{})
		}
		payload.UserData["is_first_visit"] = true
	}

	// 9. Enfileira o evento bruto no Redis de forma assíncrona
	data, err := json.Marshal(payload)
	if err == nil {
		go func(pData []byte) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = h.redis.PushRawEvent(ctx, pData)
		}(data)
	}

	// Responde 204 No Content imediatamente
	return c.SendStatus(fiber.StatusNoContent)
}

func (h *Handler) validateSiteKey(ctx context.Context, siteKey string) (string, bool) {
	// 1. Tenta memória
	if val, ok := h.siteKeys.Load(siteKey); ok {
		siteID := val.(string)
		return siteID, siteID != ""
	}

	// 2. Tenta PostgreSQL se disponível
	if h.pg != nil && h.pg.Pool != nil {
		var siteID string
		var isActive bool
		err := h.pg.Pool.QueryRow(ctx, "SELECT id::text, is_active FROM sites WHERE api_key = $1", siteKey).Scan(&siteID, &isActive)
		if err == nil && isActive {
			h.siteKeys.Store(siteKey, siteID)
			return siteID, true
		}
		if err != nil {
			fmt.Printf("[Collector] Erro ao validar site_key '%s': %v\n", siteKey, err)
		}
	}

	// Em ambiente dev ou caso o banco ainda esteja populando chaves, aceita como teste
	if h.cfg.Env == "development" || strings.HasPrefix(siteKey, "test_") {
		h.siteKeys.Store(siteKey, "00000000-0000-0000-0000-000000000001")
		return "00000000-0000-0000-0000-000000000001", true
	}

	return "", false
}

func detectDeviceType(ua string) string {
	u := strings.ToLower(ua)
	if strings.Contains(u, "tablet") || strings.Contains(u, "ipad") {
		return "tablet"
	}
	if strings.Contains(u, "mobile") || strings.Contains(u, "android") || strings.Contains(u, "iphone") {
		return "mobile"
	}
	return "desktop"
}
