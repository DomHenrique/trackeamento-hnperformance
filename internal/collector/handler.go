package collector

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
	"github.com/valyala/fasthttp"
	"tracking-engine/internal/attribution"
	"tracking-engine/internal/config"
	"tracking-engine/internal/prefixedid"
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
	ch       *storage.ClickHouseDB
	siteKeys sync.Map // Cache em memória para validação ultra-rápida de site_key
}

func NewHandler(cfg *config.Config, rdb *storage.RedisClient, pg *storage.PostgresDB, ch *storage.ClickHouseDB) *Handler {
	return &Handler{
		cfg:   cfg,
		redis: rdb,
		pg:    pg,
		ch:    ch,
	}
}

// HandleCollect recebe e processa a requisição de coleta em tempo sub-milissegundo
func (h *Handler) HandleCollect(c *fiber.Ctx) error {
	var req EventRequest
	if err := c.BodyParser(&req); err != nil {
		// Fallback para requisições vazias ou mal formatadas
		req = EventRequest{}
	}

	siteKey := prefixedid.SanitizeKey(req.SiteKey)
	if siteKey == "" {
		siteKey = prefixedid.SanitizeKey(c.Query("site_key"))
	}
	if siteKey == "" {
		siteKey = prefixedid.SanitizeKey(c.Get("X-Site-Key"))
	}

	if siteKey == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "site_key ausente",
		})
	}

	// Validação da site_key com cache
	siteMeta, valid := h.validateSiteKey(c.Context(), siteKey)
	if !valid {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "site_key invalida ou inativa",
		})
	}
	siteID := siteMeta.ID

	// 1. Gerenciamento do Cookie 1st-Party _vid
	visitorID := c.Cookies(VisitorCookieName)
	if visitorID == "" {
		if v, ok := req.UserData["visitor_id"].(string); ok && v != "" {
			visitorID = v
		}
	}

	isNewVisitor := false
	if visitorID == "" {
		visitorID = prefixedid.GenerateVisitorID()
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
		sessionID = prefixedid.GenerateSessionID()
	}

	// 5. Event ID para deduplicação (especialmente com Meta Pixel)
	eventID := req.EventID
	if eventID == "" {
		eventID = prefixedid.GenerateEventID()
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

	// Validação de Domínio de Origem (Whitelist com suporte a subdomínios)
	serverKeyHeader := strings.TrimSpace(c.Get("X-Server-Key"))
	isServerAuth := (h.cfg.ServerKey != "" && serverKeyHeader == h.cfg.ServerKey)
	originDomain := ExtractOriginDomain(c, &req, isServerAuth)
	isDev := h.cfg.Env == "development"
	if !IsDomainAllowed(originDomain, siteMeta.AllowedDomains, isDev) {
		h.RecordDomainAlert(siteID, originDomain, ip, ua, pageURL)
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
			"error":  "dominio de origem nao autorizado",
			"domain": originDomain,
		})
	}

	// 7. Extração de parâmetros de Atribuição
	attrParams := attribution.ParseURL(pageURL, referrer)

	// 8. Normalização do nome do evento
	eventName := strings.TrimSpace(req.EventName)
	if eventName == "" {
		eventName = "page_view"
	}

	// 9. Detecção de Robô e Navegações Automatizadas (Stealth Tagging)
	isBot, botReason := DetectBot(ua, eventName, req.ClientSignals)
	if isBot {
		log.Printf("[BotDetector] Robô identificado site=%s reason=%s event=%s ip=%s ua=%s", siteID, botReason, eventName, ip, ua)
		if h.redis != nil && h.redis.Client != nil {
			isConv := isConversionEvent(eventName)
			go func(r string, conv bool) {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = h.redis.Client.HIncrBy(ctx, "stats:bots:reasons", r, 1).Err()
				_ = h.redis.Client.Incr(ctx, "stats:bots:total").Err()
				if conv {
					_ = h.redis.Client.Incr(ctx, "stats:bots:protected_conversions").Err()
				}
			}(botReason, isConv)
		}
	}

	isDebug := req.IsDebug || c.Query("debug") == "true" || c.Query("debug") == "1" || c.Query("hn_debug") == "true" || c.Query("hn_debug") == "1" || c.Get("X-Debug") == "true"

	payload := EventPayload{
		EventID:       eventID,
		SiteID:        siteID,
		SiteKey:       siteKey,
		VisitorID:     visitorID,
		SessionID:     sessionID,
		EventName:     eventName,
		EventTime:     time.Now().UTC(),
		IPAddress:     ip,
		UserAgent:     ua,
		DeviceType:    deviceType,
		Attribution:   attrParams,
		UserData:      req.UserData,
		CustomData:    req.CustomData,
		ClientSignals: req.ClientSignals,
		IsBot:         isBot,
		BotReason:     botReason,
		IsDebug:       isDebug,
		CreatedAt:     time.Now().UTC(),
	}

	if isNewVisitor {
		if payload.UserData == nil {
			payload.UserData = make(map[string]interface{})
		}
		payload.UserData["is_first_visit"] = true
	}

	// 9. Roteamento: se for evento de depuração (debug), transmite para o canal do DebugView e buffer volátil,
	// DESCARTANDO da fila analítica do ClickHouse (zero poluição de dados e relatórios de clientes).
	data, err := json.Marshal(payload)
	if err == nil {
		if isDebug {
			go func(pData []byte, sID string) {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				if h.redis != nil {
					_ = h.redis.PublishDebugEvent(ctx, sID, pData)
					_ = h.redis.PushDebugBuffer(ctx, sID, pData)
				}
			}(data, siteID)
		} else {
			go func(pData []byte) {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				if h.redis != nil {
					_ = h.redis.PushRawEvent(ctx, pData)
				}
			}(data)
		}
	}

	// Responde 204 No Content imediatamente
	return c.SendStatus(fiber.StatusNoContent)
}

func (h *Handler) validateSiteKey(ctx context.Context, siteKey string) (*SiteMetadata, bool) {
	// 1. Tenta memória
	if val, ok := h.siteKeys.Load(siteKey); ok {
		meta := val.(*SiteMetadata)
		return meta, meta != nil && meta.ID != ""
	}

	// 2. Tenta PostgreSQL se disponível
	if h.pg != nil && h.pg.Pool != nil {
		var siteID string
		var isActive bool
		err := h.pg.Pool.QueryRow(ctx, "SELECT id::text, is_active FROM sites WHERE api_key = $1", siteKey).Scan(&siteID, &isActive)
		if err == nil && isActive {
			domains := []string{}
			rows, errRows := h.pg.Pool.Query(ctx, "SELECT domain FROM site_allowed_domains WHERE site_id = $1 AND is_active = true", siteID)
			if errRows == nil {
				defer rows.Close()
				for rows.Next() {
					var d string
					if errScan := rows.Scan(&d); errScan == nil && d != "" {
						domains = append(domains, d)
					}
				}
			}

			meta := &SiteMetadata{
				ID:             siteID,
				AllowedDomains: domains,
			}
			h.siteKeys.Store(siteKey, meta)
			return meta, true
		}
		if err != nil {
			fmt.Printf("[Collector] Erro ao validar site_key '%s': %v\n", siteKey, err)
		}
	}

	// Em ambiente dev, aceita chaves de teste com prefixo test_
	if h.cfg.Env == "development" && strings.HasPrefix(siteKey, "test_") {
		meta := &SiteMetadata{
			ID:             "00000000-0000-0000-0000-000000000001",
			AllowedDomains: []string{"localhost", "127.0.0.1"},
		}
		h.siteKeys.Store(siteKey, meta)
		return meta, true
	}

	return nil, false
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

func isConversionEvent(eventName string) bool {
	name := strings.ToLower(strings.TrimSpace(eventName))
	return name == "lead" || name == "purchase" || name == "whatsapp_click" || name == "form_submit" || name == "contact"
}

// HandleDebugStream gerencia a conexão Server-Sent Events (SSE) para entrega em tempo real no DebugView
func (h *Handler) HandleDebugStream(c *fiber.Ctx) error {
	siteID := strings.TrimSpace(c.Query("site_id"))
	if siteID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "site_id é obrigatório",
		})
	}

	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("Transfer-Encoding", "chunked")

	c.Context().SetBodyStreamWriter(fasthttp.StreamWriter(func(w *bufio.Writer) {
		// 1. Envia eventos recentes do buffer volátil do Redis (histórico recente)
		if h.redis != nil {
			ctxHistory, cancelHistory := context.WithTimeout(context.Background(), 2*time.Second)
			recent, err := h.redis.GetRecentDebugEvents(ctxHistory, siteID, 30)
			cancelHistory()
			if err == nil {
				// Envia os mais antigos primeiro para a timeline exibir cronologicamente
				for i := len(recent) - 1; i >= 0; i-- {
					fmt.Fprintf(w, "event: message\ndata: %s\n\n", recent[i])
				}
				_ = w.Flush()
			}
		}

		// 2. Subscrição Pub/Sub para eventos em tempo real
		var pubsub *redis.PubSub
		if h.redis != nil {
			pubsub = h.redis.SubscribeDebug(context.Background(), siteID)
			defer pubsub.Close()
		}

		ch := make(chan *redis.Message, 100)
		if pubsub != nil {
			go func() {
				for msg := range pubsub.Channel() {
					ch <- msg
				}
			}()
		}

		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case msg, ok := <-ch:
				if !ok {
					return
				}
				fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg.Payload)
				if err := w.Flush(); err != nil {
					return // Conexão encerrada pelo cliente
				}
			case <-ticker.C:
				fmt.Fprintf(w, ": ping\n\n")
				if err := w.Flush(); err != nil {
					return // Conexão encerrada pelo cliente
				}
			}
		}
	}))

	return nil
}

// DebugSimulateRequest estrutura os dados para envio de evento simulado
type DebugSimulateRequest struct {
	SiteID     string                 `json:"site_id"`
	EventName  string                 `json:"event_name"`
	PageURL    string                 `json:"page_url"`
	UserData   map[string]interface{} `json:"user_data"`
	CustomData map[string]interface{} `json:"custom_data"`
}

// HandleDebugSimulate gera e injeta um evento de teste sintético no canal de streaming do site
func (h *Handler) HandleDebugSimulate(c *fiber.Ctx) error {
	var req DebugSimulateRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "payload inválido",
		})
	}

	req.SiteID = strings.TrimSpace(req.SiteID)
	if req.SiteID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "site_id é obrigatório",
		})
	}

	eventName := strings.TrimSpace(req.EventName)
	if eventName == "" {
		eventName = "test_event"
	}

	eventID := prefixedid.GenerateEventID()
	visitorID := prefixedid.GenerateVisitorID()
	sessionID := prefixedid.GenerateSessionID()
	pageURL := strings.TrimSpace(req.PageURL)
	if pageURL == "" {
		pageURL = "https://simulator.local/debug"
	}

	attrParams := attribution.ParseURL(pageURL, "")

	payload := EventPayload{
		EventID:       eventID,
		SiteID:        req.SiteID,
		SiteKey:       "simulation_key",
		VisitorID:     visitorID,
		SessionID:     sessionID,
		EventName:     eventName,
		EventTime:     time.Now().UTC(),
		IPAddress:     c.IP(),
		UserAgent:     "HN-Debug-Simulator/1.0 (Web UI)",
		DeviceType:    "desktop",
		Attribution:   attrParams,
		UserData:      req.UserData,
		CustomData:    req.CustomData,
		ClientSignals: &ClientSignals{
			ScreenW:          1920,
			ScreenH:          1080,
			TimeToInteractMs: 1200,
			TimeOnPageMs:     5000,
		},
		IsBot:     false,
		BotReason: "",
		IsDebug:   true,
		CreatedAt: time.Now().UTC(),
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "erro ao serializar evento simulado",
		})
	}

	if h.redis != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.redis.PublishDebugEvent(ctx, req.SiteID, data)
		_ = h.redis.PushDebugBuffer(ctx, req.SiteID, data)
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"status":   "success",
		"event_id": eventID,
		"payload":  payload,
	})
}

// HandleDebugClear limpa o buffer de eventos de depuração do site
func (h *Handler) HandleDebugClear(c *fiber.Ctx) error {
	siteID := strings.TrimSpace(c.Query("site_id"))
	if siteID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "site_id é obrigatório",
		})
	}

	if h.redis != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		key := fmt.Sprintf("debug:events:%s", siteID)
		_ = h.redis.Client.Del(ctx, key).Err()
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"status":  "cleared",
		"site_id": siteID,
	})
}
