package collector

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
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
	if len(c.Body()) > 64*1024 {
		return c.Status(fiber.StatusRequestEntityTooLarge).JSON(fiber.Map{
			"error": "payload excede o limite máximo permitido (64KB)",
		})
	}

	var req EventRequest
	if err := c.BodyParser(&req); err != nil {
		// Fallback para requisições vazias ou mal formatadas
		req = EventRequest{}
	}
	if req.SiteKey == "" && len(c.Body()) > 0 {
		_ = json.Unmarshal(c.Body(), &req)
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

	// 1. Gerenciamento de Governança de Privacidade e Consentimento
	gpcHeader := strings.TrimSpace(c.Get("Sec-GPC"))
	dntHeader := strings.TrimSpace(c.Get("DNT"))
	isGPC := gpcHeader == "1"
	isDNT := dntHeader == "1"

	privacy := siteMeta.PrivacySettings
	if privacy == nil {
		privacy = DefaultPrivacySettings()
	}

	consent := ConsentState{
		Necessary: true,
		Analytics: false,
		Marketing: false,
	}

	analyticsPolicy := privacy.CategoriesPolicy["analytics"]
	marketingPolicy := privacy.CategoriesPolicy["marketing"]

	if req.Consent != nil {
		// Preferência explícita do usuário enviada pelo SDK/CMP
		consent.Analytics = req.Consent.Analytics
		consent.Marketing = req.Consent.Marketing
	} else {
		// Se não foi informada escolha explícita, respeita a política configurada para o site
		consent.Analytics = !analyticsPolicy.RequiresConsent
		consent.Marketing = !marketingPolicy.RequiresConsent
	}

	// Se GPC/DNT estiver ativo e a política mandar respeitar, revoga marketing
	if (isGPC || isDNT) && privacy.EnforceGPC {
		consent.Marketing = false
	}

	// 2. Gerenciamento do Cookie 1st-Party _vid (Condicionado a consentimento e política)
	allowsCookie := consent.Analytics && (analyticsPolicy.IssueVisitorCookie || !analyticsPolicy.RequiresConsent)

	rawCookieVid := strings.TrimSpace(c.Cookies(VisitorCookieName))
	var visitorID string
	isNewVisitor := false

	if rawCookieVid != "" && prefixedid.IsValidVisitorID(rawCookieVid) {
		visitorID = rawCookieVid
	} else if allowsCookie {
		// Se ausente, inválido ou corrompido, gera nova identidade canônica no servidor.
		// Identificadores arbitrários em req.UserData["visitor_id"] são expressamente desconsiderados
		// para evitar Session Fixation e Cookie Poisoning.
		visitorID = prefixedid.GenerateVisitorID()
		isNewVisitor = true
	} else {
		// Sem autorização para cookie analítico: visitante opera sem persistência de identidade
		visitorID = ""
	}

	if req.UserData != nil && visitorID != "" {
		req.UserData["visitor_id"] = visitorID
	}

	// Emite o cookie apenas se a política e o consentimento permitirem
	if allowsCookie && visitorID != "" {
		c.Cookie(&fiber.Cookie{
			Name:     VisitorCookieName,
			Value:    visitorID,
			MaxAge:   OneYearSeconds,
			Path:     "/",
			SameSite: "Lax",
			Secure:   h.cfg.Env == "production",
			HTTPOnly: false, // Permite acesso pelo SDK JavaScript se necessário
		})
	}

	// 3. Extração de IP Real Confiável (com validação de TRUSTED_PROXIES e mascaramento de privacidade)
	rawIP := ResolveClientIP(c, h.cfg)
	ip := rawIP
	if privacy.MaskIP {
		ip = MaskIP(rawIP)
	}

	// 4. User-Agent e Tipo de Dispositivo
	ua := c.Get("User-Agent")
	deviceType := detectDeviceType(ua)

	// 5. Session ID
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = prefixedid.GenerateSessionID()
	}

	// 6. Event ID para deduplicação (especialmente com Meta Pixel)
	eventID := req.EventID
	if eventID == "" {
		eventID = prefixedid.GenerateEventID()
	}

	// 7. URL e Referrer
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

	// 8. Extração de parâmetros de Atribuição
	attrParams := attribution.ParseURL(pageURL, referrer)

	// 9. Normalização do nome do evento
	eventName := strings.TrimSpace(req.EventName)
	if eventName == "" {
		eventName = "page_view"
	}

	// 10. Detecção de Robô e Navegações Automatizadas (Stealth Tagging)
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

	originMode := "production"
	if isDebug {
		originMode = "debug"
	}

	payload := EventPayload{
		EventID:        eventID,
		SiteID:         siteID,
		SiteKey:        siteKey,
		VisitorID:      visitorID,
		SessionID:      sessionID,
		EventName:      eventName,
		EventTime:      time.Now().UTC(),
		IPAddress:      ip,
		UserAgent:      ua,
		DeviceType:     deviceType,
		Attribution:    attrParams,
		UserData:       req.UserData,
		CustomData:     req.CustomData,
		ClientSignals:  req.ClientSignals,
		Consent:        consent,
		PrivacySignals: PrivacySignals{GPC: isGPC, DNT: isDNT},
		IsBot:          isBot,
		BotReason:      botReason,
		IsDebug:        isDebug,
		OriginMode:     originMode,
		CreatedAt:      time.Now().UTC(),
	}

	if isNewVisitor {
		if payload.UserData == nil {
			payload.UserData = make(map[string]interface{})
		}
		payload.UserData["is_first_visit"] = true
	}

	// 9. Roteamento:
	// - Se for evento de depuração (debug), transmite para o canal do DebugView e buffer volátil,
	//   DESCARTANDO da fila analítica do ClickHouse (zero poluição de dados e relatórios de clientes).
	// - Se for evento de produção, enfileira para o ClickHouse E TAMBÉM transmite para o canal do DebugView
	//   com a marcação origin_mode="production", permitindo depuração em tempo real mesmo sem a flag ?hn_debug.
	data, err := json.Marshal(payload)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "falha ao serializar evento",
		})
	}

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
		// Ingestão síncrona no stream principal com timeout estrito de 400ms
		if h.redis != nil {
			pushCtx, pushCancel := context.WithTimeout(c.Context(), 400*time.Millisecond)
			defer pushCancel()

			if err := h.redis.PushRawEvent(pushCtx, data); err != nil {
				return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
					"error": "falha ao persistir evento no Redis Stream",
				})
			}
		} else if h.cfg == nil || h.cfg.Env != "test" {
			// Em produção e staging, ausência de cliente Redis impede aceitação do evento
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"error": "serviço de enfileiramento indisponível",
			})
		}

		// Emissões auxiliares para o DebugView continuam em background
		go func(pData []byte, sID string) {
			bgCtx, bgCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer bgCancel()
			if h.redis != nil {
				_ = h.redis.PublishDebugEvent(bgCtx, sID, pData)
				_ = h.redis.PushDebugBuffer(bgCtx, sID, pData)
			}
		}(data, siteID)
	}

	// Responde 202 Accepted com confirmação explícita e identificador único
	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
		"status":   "accepted",
		"event_id": eventID,
	})
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
		var rawPrivacy []byte

		// 2.1 Primeiro tenta na tabela site_api_keys (suporte a múltiplas chaves e revogação)
		err := h.pg.Pool.QueryRow(ctx, `
			SELECT k.site_id::text, (k.status = 'active' AND s.is_active = true), COALESCE(s.privacy_settings, '{}'::jsonb)
			FROM site_api_keys k
			JOIN sites s ON s.id = k.site_id
			WHERE k.key = $1
		`, siteKey).Scan(&siteID, &isActive, &rawPrivacy)

		// 2.2 Fallback retroativo para sites.api_key caso ainda não esteja em site_api_keys
		if err != nil {
			err = h.pg.Pool.QueryRow(ctx, "SELECT id::text, is_active, COALESCE(privacy_settings, '{}'::jsonb) FROM sites WHERE api_key = $1", siteKey).Scan(&siteID, &isActive, &rawPrivacy)
		}

		if err == nil && isActive {
			domains := []string{}
			// Inclui tanto o domínio raiz de sites.domain quanto os cadastrados em site_allowed_domains
			rows, errRows := h.pg.Pool.Query(ctx, `
				SELECT DISTINCT LOWER(TRIM(d)) FROM (
					SELECT domain AS d FROM sites WHERE id = $1
					UNION
					SELECT domain AS d FROM site_allowed_domains WHERE site_id = $1 AND is_active = true
				) sub WHERE d IS NOT NULL AND d != ''
			`, siteID)
			if errRows == nil {
				defer rows.Close()
				for rows.Next() {
					var d string
					if errScan := rows.Scan(&d); errScan == nil && d != "" {
						domains = append(domains, d)
					}
				}
			}

			privacy := DefaultPrivacySettings()
			if len(rawPrivacy) > 2 {
				_ = json.Unmarshal(rawPrivacy, privacy)
			}

			meta := &SiteMetadata{
				ID:              siteID,
				AllowedDomains:  domains,
				PrivacySettings: privacy,
			}
			h.siteKeys.Store(siteKey, meta)

			// Atualiza telemetria da chave assincronamente sem bloquear a requisição
			go func(sk string) {
				upCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_, _ = h.pg.Pool.Exec(upCtx, `
					UPDATE site_api_keys 
					SET last_used_at = now(), total_events_count = total_events_count + 1 
					WHERE key = $1
				`, sk)
			}(siteKey)

			return meta, true
		}
		if err != nil {
			fmt.Printf("[Collector] Erro ao validar site_key '%s': %v\n", siteKey, err)
		}
	}

	// Em ambiente dev, aceita chaves de teste com prefixo test_
	if h.cfg.Env == "development" && strings.HasPrefix(siteKey, "test_") {
		meta := &SiteMetadata{
			ID:              "00000000-0000-0000-0000-000000000001",
			AllowedDomains:  []string{"localhost", "127.0.0.1"},
			PrivacySettings: DefaultPrivacySettings(),
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
			recent, err := h.redis.GetRecentDebugEvents(ctxHistory, siteID, 50)
			cancelHistory()
			if err == nil {
				// Envia os mais antigos primeiro para a timeline exibir cronologicamente
				for i := len(recent) - 1; i >= 0; i-- {
					fmt.Fprintf(w, "data: %s\n\n", recent[i])
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
				fmt.Fprintf(w, "data: %s\n\n", msg.Payload)
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

// HandleGetDebugEvents retorna os eventos recentes gravados no Redis em formato JSON via REST para inicialização instantânea
func (h *Handler) HandleGetDebugEvents(c *fiber.Ctx) error {
	siteID := strings.TrimSpace(c.Query("site_id"))
	if siteID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "site_id é obrigatório",
		})
	}

	if h.redis == nil {
		return c.Status(fiber.StatusOK).JSON([]interface{}{})
	}

	ctx, cancel := context.WithTimeout(c.Context(), 2*time.Second)
	defer cancel()

	recent, err := h.redis.GetRecentDebugEvents(ctx, siteID, 50)
	if err != nil {
		return c.Status(fiber.StatusOK).JSON([]interface{}{})
	}

	events := make([]map[string]interface{}, 0, len(recent))
	for _, raw := range recent {
		var ev map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &ev); err == nil {
			events = append(events, ev)
		}
	}

	return c.Status(fiber.StatusOK).JSON(events)
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

// ResolveClientIP extrai o IP real do cliente aplicando validação estrita contra TRUSTED_PROXIES
func ResolveClientIP(c *fiber.Ctx, cfg *config.Config) string {
	remoteIP := c.Context().RemoteIP()
	remoteIPStr := c.IP()
	if len(remoteIP) > 0 {
		remoteIPStr = remoteIP.String()
	}

	// Se não houver configuração ou se a conexão direta não vier de um proxy confiável,
	// ignora categoricamente os cabeçalhos de proxy para prevenir IP spoofing
	if cfg == nil || !cfg.IsTrustedProxy(remoteIP) {
		return remoteIPStr
	}

	// Conexão TCP imediata veio de um proxy confiável; avalia os cabeçalhos encaminhados
	// 1. Cloudflare
	if cfIP := strings.TrimSpace(c.Get("CF-Connecting-IP")); cfIP != "" {
		if parsed := net.ParseIP(cfIP); parsed != nil {
			return cfIP
		}
	}

	// 2. Nginx / Traefik
	if realIP := strings.TrimSpace(c.Get("X-Real-IP")); realIP != "" {
		if parsed := net.ParseIP(realIP); parsed != nil {
			return realIP
		}
	}

	// 3. X-Forwarded-For (primeiro IP válido da cadeia)
	if xff := strings.TrimSpace(c.Get("X-Forwarded-For")); xff != "" {
		parts := strings.Split(xff, ",")
		for _, part := range parts {
			cleanPart := strings.TrimSpace(part)
			if cleanPart != "" {
				if parsed := net.ParseIP(cleanPart); parsed != nil {
					return cleanPart
				}
			}
		}
	}

	return remoteIPStr
}
