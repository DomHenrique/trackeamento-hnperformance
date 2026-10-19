package collector

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
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

	RedisKeyTelemetryCounts = "telemetry:site_keys:counts"
	RedisKeyTelemetryTimes  = "telemetry:site_keys:times"
)

type Handler struct {
	cfg               *config.Config
	redis             *storage.RedisClient
	pg                *storage.PostgresDB
	ch                *storage.ClickHouseDB
	identityMgr       *VisitorIdentityManager
	siteKeys          sync.Map // Cache em memória para validação ultra-rápida de site_key com TTL
	serverKeys        sync.Map // Cache em memória para validação de chaves de servidor hn_sec_ com TTL
	keyThrottle       sync.Map // Throttle em memória para timestamp de telemetria
	pageCatalogCache  sync.Map // Throttle em memória para auto-registro de páginas no catálogo
	offlineReconciler OfflineReconcileFunc
	inboundWorker     *InboundWorker
	inboundPersister  InboundPersisterFunc
}

func NewHandler(cfg *config.Config, rdb *storage.RedisClient, pg *storage.PostgresDB, ch *storage.ClickHouseDB) *Handler {
	h := &Handler{
		cfg:         cfg,
		redis:       rdb,
		pg:          pg,
		ch:          ch,
		identityMgr: NewVisitorIdentityManager(rdb),
	}
	h.inboundWorker = NewInboundWorker(pg, rdb, nil)
	return h
}

// StartInboundWorker inicia o processador assíncrono durável de webhooks de CRM/ERP
func (h *Handler) StartInboundWorker(ctx context.Context) {
	if h.inboundWorker != nil {
		h.inboundWorker.Start(ctx)
	}
}

// StopInboundWorker finaliza graciosamente o consumidor de CRM/ERP
func (h *Handler) StopInboundWorker() {
	if h.inboundWorker != nil {
		h.inboundWorker.Stop()
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

	var siteMeta *SiteMetadata
	var valid bool

	if siteKey != "" {
		siteMeta, valid = h.validateSiteKey(c.Context(), siteKey)
		if !valid {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "site_key invalida ou inativa",
			})
		}
	} else {
		// Fallback para First-Party Ingress Gateway: Resolução por cabeçalho Host
		host := c.Hostname()
		if fwdHost := c.Get("X-Forwarded-Host"); fwdHost != "" {
			host = fwdHost
		}
		siteMeta, valid = h.resolveSiteByHost(c.Context(), host)
		if !valid || siteMeta == nil {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "site_key ausente e host nao cadastrado no gateway",
			})
		}
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

	hasActiveLeadConsent := req.ExplicitLeadConsent
	if !hasActiveLeadConsent && req.CustomData != nil {
		if val, ok := req.CustomData["explicit_lead_consent"].(bool); ok && val {
			hasActiveLeadConsent = true
		}
	}
	if !hasActiveLeadConsent && req.UserData != nil {
		if em, ok := req.UserData["email"].(string); ok && strings.TrimSpace(em) != "" {
			hasActiveLeadConsent = true
		}
		if ph, ok := req.UserData["phone"].(string); ok && strings.TrimSpace(ph) != "" {
			hasActiveLeadConsent = true
		}
	}

	if req.Consent != nil {
		// Preferência explícita do usuário enviada pelo SDK/CMP
		consent.Analytics = req.Consent.Analytics
		consent.Marketing = req.Consent.Marketing
	} else {
		// Se não foi informada escolha explícita, respeita a política configurada para o site
		consent.Analytics = !analyticsPolicy.RequiresConsent
		consent.Marketing = !marketingPolicy.RequiresConsent
	}

	// Se GPC/DNT estiver ativo e a política mandar respeitar, revoga marketing para navegações passivas,
	// mas PRESERVA a autorização se houver manifestação voluntária de formulário/lead
	if (isGPC || isDNT) && privacy.EnforceGPC {
		if !hasActiveLeadConsent {
			consent.Marketing = false
		} else {
			consent.Marketing = true
		}
	} else if hasActiveLeadConsent {
		consent.Marketing = true
	}

	// 2. Extração de IP Real Confiável (com validação de TRUSTED_PROXIES e mascaramento de privacidade)
	rawIP := ResolveClientIP(c, h.cfg)
	ip := rawIP
	if privacy.MaskIP {
		ip = MaskIP(rawIP)
	}

	// 3. User-Agent e Tipo de Dispositivo
	ua := c.Get("User-Agent")
	deviceType := detectDeviceType(ua)

	// 4. Resolução de Identidade do Visitante (1st-Party Storage ou Cookieless Server-Side Determinístico)
	if h.identityMgr == nil {
		h.identityMgr = NewVisitorIdentityManager(h.redis)
	}
	visitorID := ""
	if req.VisitorID != "" && strings.HasPrefix(req.VisitorID, prefixedid.PrefixVisitor) {
		visitorID = strings.TrimSpace(req.VisitorID)
	} else if vid, ok := req.UserData["visitor_id"].(string); ok && strings.HasPrefix(vid, prefixedid.PrefixVisitor) {
		visitorID = strings.TrimSpace(vid)
	}
	if visitorID == "" {
		visitorID = h.identityMgr.ResolveVisitorID(c.Context(), time.Now(), siteID, ip, ua, req.ClientSignals)
	}
	isNewVisitor := false

	if req.UserData == nil {
		req.UserData = make(map[string]interface{})
	}
	req.UserData["visitor_id"] = visitorID

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

	// Validação de Domínio de Origem (Whitelist com suporte a subdomínios - Abordagem B)
	serverKeyHeader := strings.TrimSpace(c.Get("X-Server-Key"))
	isServerAuth := (h.cfg.ServerKey != "" && serverKeyHeader == h.cfg.ServerKey)
	originDomain := ExtractOriginDomain(c, &req, isServerAuth, siteMeta.CnameSubdomain)
	isDev := h.cfg.Env == "development"
	if !IsDomainAllowed(originDomain, siteMeta.AllowedDomains, isDev) {
		h.RecordDomainAlert(siteID, originDomain, ip, ua, pageURL)
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
			"error":  "dominio de origem nao autorizado",
			"domain": originDomain,
		})
	}

	// 8. Extração e Herança de Atribuição de Sessão (Convenção GA4)
	opts := attribution.Options{
		OwnHosts: append([]string{siteMeta.Domain}, siteMeta.AllowedDomains...),
	}
	attrParams := attribution.ParseURLWithOptions(pageURL, referrer, opts)

	hasOwnSignal := (attrParams.Channel != attribution.ChannelDirect && attrParams.Channel != attribution.ChannelNotSet) ||
		attrParams.HasClickID() || attrParams.UTMSource != "" || attrParams.UTMCampaign != ""

	sessionKey := fmt.Sprintf("tracking:session_attr:%s:%s", siteID, sessionID)

	if hasOwnSignal {
		attrParams.Inherited = 0
		if h.redis != nil && h.redis.Client != nil && sessionID != "" {
			go func(k string, params attribution.Params) {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				if data, err := json.Marshal(params); err == nil {
					_ = h.redis.Client.Set(ctx, k, data, 30*time.Minute).Err()
				}
			}(sessionKey, attrParams)
		}
	} else {
		inherited := false
		// 1. Tenta herdar do SessionContext reenviado pelo SDK
		if req.SessionContext != nil && strings.TrimSpace(req.SessionContext.EntryURL) != "" {
			entryAttr := attribution.ParseURLWithOptions(req.SessionContext.EntryURL, req.SessionContext.EntryReferrer, opts)
			if (entryAttr.Channel != attribution.ChannelDirect && entryAttr.Channel != attribution.ChannelNotSet) || entryAttr.HasClickID() {
				attrParams = entryAttr
				attrParams.Inherited = 1
				inherited = true
			}
		}

		// 2. Se o SDK não enviou contexto válido, tenta recuperar do cache do Redis
		if !inherited && h.redis != nil && h.redis.Client != nil && sessionID != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			cachedData, err := h.redis.Client.Get(ctx, sessionKey).Bytes()
			cancel()
			if err == nil && len(cachedData) > 0 {
				var cachedAttr attribution.Params
				if err := json.Unmarshal(cachedData, &cachedAttr); err == nil {
					attrParams = cachedAttr
					attrParams.Inherited = 1
					// Renova TTL de forma leve sem reescrita quente
					go func(k string) {
						rCtx, rCancel := context.WithTimeout(context.Background(), 1*time.Second)
						defer rCancel()
						_ = h.redis.Client.Expire(rCtx, k, 30*time.Minute).Err()
					}(sessionKey)
				}
			}
		}
	}

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

	submissionID := req.SubmissionID
	formLifecycleState := req.FormLifecycleState
	fieldSource := req.FieldSource
	if req.CustomData != nil {
		if submissionID == "" {
			if s, ok := req.CustomData["submission_id"].(string); ok {
				submissionID = s
			}
		}
		if formLifecycleState == "" {
			if s, ok := req.CustomData["form_lifecycle_state"].(string); ok {
				formLifecycleState = s
			}
		}
		if fieldSource == "" {
			if s, ok := req.CustomData["field_source"].(string); ok {
				fieldSource = s
			}
		}
	}

	canonicalURL := req.CanonicalURL
	if canonicalURL == "" && req.CustomData != nil {
		if cURL, ok := req.CustomData["canonical_url"].(string); ok {
			canonicalURL = cURL
		}
	}

	canSettings := siteMeta.CanonicalSettings
	if canSettings == nil {
		canSettings = DefaultCanonicalSettings(siteMeta.Domain)
	}
	pageKey := canSettings.ValidateAndResolveCanonical(pageURL, canonicalURL, siteMeta.AllowedDomains...)

	payload := EventPayload{
		EventID:            eventID,
		SiteID:             siteID,
		SiteKey:            siteKey,
		VisitorID:          visitorID,
		SessionID:          sessionID,
		EventName:          eventName,
		EventTime:          time.Now().UTC(),
		IPAddress:          ip,
		UserAgent:          ua,
		DeviceType:         deviceType,
		Attribution:        attrParams,
		PageKey:            pageKey,
		CanonicalURL:       canonicalURL,
		UserData:           req.UserData,
		CustomData:         req.CustomData,
		SubmissionID:       submissionID,
		FormLifecycleState: formLifecycleState,
		FieldSource:        fieldSource,
		ClientSignals:      req.ClientSignals,
		Consent:            consent,
		ExplicitLeadConsent: hasActiveLeadConsent,
		PrivacySignals:     PrivacySignals{GPC: isGPC, DNT: isDNT},
		IsBot:              isBot,
		BotReason:          botReason,
		IsDebug:            isDebug,
		OriginMode:         originMode,
		CreatedAt:          time.Now().UTC(),
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

	// Prepara payload com PII estritamente mascarada para o canal de DebugView
	// Garante que logs de depuração em tempo real nunca exibam emails, telefones ou nomes em claro
	debugPayload := payload
	if payload.UserData != nil {
		debugPayload.UserData = maskUserData(payload.UserData)
	}
	debugData, _ := json.Marshal(debugPayload)

	if isDebug {
		go func(pData []byte, sID string) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if h.redis != nil {
				_ = h.redis.PublishDebugEvent(ctx, sID, pData)
				_ = h.redis.PushDebugBuffer(ctx, sID, pData)
			}
		}(debugData, siteID)
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

		// Emissões auxiliares para o DebugView continuam em background com PII mascarada
		go func(pData []byte, sID string) {
			bgCtx, bgCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer bgCancel()
			if h.redis != nil {
				_ = h.redis.PublishDebugEvent(bgCtx, sID, pData)
				_ = h.redis.PushDebugBuffer(bgCtx, sID, pData)
			}
		}(debugData, siteID)
	}

	// Auto-cadastra página descoberta por tráfego real no catálogo do site de forma assíncrona
	if pageKey != "" && siteID != "" && h.pg != nil && h.pg.Pool != nil {
		cacheKey := siteID + ":" + pageKey
		if _, seen := h.pageCatalogCache.Load(cacheKey); !seen {
			h.pageCatalogCache.Store(cacheKey, time.Now())
			title := ""
			if req.CustomData != nil {
				if t, ok := req.CustomData["title"].(string); ok {
					title = t
				} else if t, ok := req.CustomData["page_title"].(string); ok {
					title = t
				}
			}
			go func(sID, pKey, cURL, t string) {
				bgCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = h.autoRegisterTrafficPage(bgCtx, sID, pKey, cURL, t)
			}(siteID, pageKey, canonicalURL, title)
		}
	}

	// Atualiza telemetria da chave de acesso de forma desacoplada
	if siteKey != "" {
		h.RecordKeyUsage(siteKey)
	}

	// Responde 202 Accepted com confirmação explícita e identificador único
	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
		"status":   "accepted",
		"event_id": eventID,
	})
}

func (h *Handler) validateSiteKey(ctx context.Context, siteKey string) (*SiteMetadata, bool) {
	// 1. Tenta memória com verificação de TTL
	if val, ok := h.siteKeys.Load(siteKey); ok {
		if cached, ok := val.(*CachedSiteKey); ok {
			if time.Now().Before(cached.ExpiresAt) {
				return cached.Metadata, cached.Metadata != nil && cached.Metadata.ID != ""
			}
			// Expirou pelo TTL de 5 minutos: remove para revalidar com o banco
			h.siteKeys.Delete(siteKey)
		} else if meta, ok := val.(*SiteMetadata); ok {
			return meta, meta != nil && meta.ID != ""
		}
	}

	// 2. Tenta PostgreSQL se disponível
	if h.pg != nil && h.pg.Pool != nil {
		var keyID string
		var siteID string
		var isActive bool
		var rawPrivacy []byte
		var cnameSub string
		var rawCanonical []byte
		var primaryDomain string

		// 2.1 Primeiro tenta na tabela site_api_keys (suporte a múltiplas chaves e revogação)
		err := h.pg.Pool.QueryRow(ctx, `
			SELECT k.id::text, k.site_id::text, (k.status = 'active' AND s.is_active = true), COALESCE(s.privacy_settings, '{}'::jsonb), COALESCE(s.cname_subdomain, ''), COALESCE(s.canonical_settings, '{}'::jsonb), s.domain
			FROM site_api_keys k
			JOIN sites s ON s.id = k.site_id
			WHERE k.key = $1
		`, siteKey).Scan(&keyID, &siteID, &isActive, &rawPrivacy, &cnameSub, &rawCanonical, &primaryDomain)

		// 2.2 Fallback retroativo para sites.api_key caso ainda não esteja em site_api_keys
		if err != nil {
			err = h.pg.Pool.QueryRow(ctx, "SELECT id::text, is_active, COALESCE(privacy_settings, '{}'::jsonb), COALESCE(cname_subdomain, ''), COALESCE(canonical_settings, '{}'::jsonb), domain FROM sites WHERE api_key = $1", siteKey).Scan(&siteID, &isActive, &rawPrivacy, &cnameSub, &rawCanonical, &primaryDomain)
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

			canSettings := DefaultCanonicalSettings(primaryDomain)
			if len(rawCanonical) > 2 {
				_ = json.Unmarshal(rawCanonical, canSettings)
			}
			if canSettings.CanonicalHost == "" && primaryDomain != "" {
				canSettings.CanonicalHost = cleanHost(primaryDomain)
			}

			meta := &SiteMetadata{
				ID:                siteID,
				KeyID:             keyID,
				Domain:            primaryDomain,
				AllowedDomains:    domains,
				PrivacySettings:   privacy,
				CanonicalSettings: canSettings,
				CnameSubdomain:    cnameSub,
			}
			h.siteKeys.Store(siteKey, &CachedSiteKey{
				KeyID:     keyID,
				Metadata:  meta,
				ExpiresAt: time.Now().Add(5 * time.Minute),
			})

			// Contabiliza telemetria desacoplada
			h.RecordKeyUsage(siteKey)

			return meta, true
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

// RecordKeyUsage registra o uso da chave de acesso no Redis Hash sem abrir transação ou bloquear o Postgres
func (h *Handler) RecordKeyUsage(siteKey string) {
	if siteKey == "" {
		return
	}

	if h.redis != nil && h.redis.Client != nil {
		go func(sk string) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			_ = h.redis.Client.HIncrBy(ctx, RedisKeyTelemetryCounts, sk, 1).Err()

			// Throttle de timestamp: grava no Redis apenas a cada 30 segundos por chave
			now := time.Now()
			lastVal, ok := h.keyThrottle.Load(sk)
			if !ok || now.Sub(lastVal.(time.Time)) >= 30*time.Second {
				h.keyThrottle.Store(sk, now)
				_ = h.redis.Client.HSet(ctx, RedisKeyTelemetryTimes, sk, now.Unix()).Err()
			}
		}(siteKey)
	}
}

// StartTelemetryFlushWorker executa periodicamente o descarregamento das métricas do Redis para o PostgreSQL
func (h *Handler) StartTelemetryFlushWorker(ctx context.Context, interval time.Duration) {
	if h.redis == nil || h.redis.Client == nil || h.pg == nil || h.pg.Pool == nil {
		return
	}
	if interval <= 0 {
		interval = 15 * time.Second
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		workerID := uuid.New().String()[:8]

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.FlushTelemetryToPostgres(ctx, workerID)
			}
		}
	}()
}

// FlushTelemetryToPostgres renomeia atomicamente o Hash no Redis e atualiza o PostgreSQL em lote com GREATEST
func (h *Handler) FlushTelemetryToPostgres(ctx context.Context, workerID string) {
	if h.redis == nil || h.redis.Client == nil || h.pg == nil || h.pg.Pool == nil {
		return
	}

	flushCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	procCountsKey := fmt.Sprintf("telemetry:site_keys:counts:proc:%s", workerID)
	procTimesKey := fmt.Sprintf("telemetry:site_keys:times:proc:%s", workerID)

	// 1. RENAME atômico de contadores
	err := h.redis.Client.Rename(flushCtx, RedisKeyTelemetryCounts, procCountsKey).Err()
	if err != nil {
		return
	}

	hasTimes := false
	if errTimes := h.redis.Client.Rename(flushCtx, RedisKeyTelemetryTimes, procTimesKey).Err(); errTimes == nil {
		hasTimes = true
	}

	// 2. Lê contadores acumulados
	counts, err := h.redis.Client.HGetAll(flushCtx, procCountsKey).Result()
	if err != nil || len(counts) == 0 {
		_ = h.redis.Client.Del(flushCtx, procCountsKey).Err()
		if hasTimes {
			_ = h.redis.Client.Del(flushCtx, procTimesKey).Err()
		}
		return
	}

	// Lê timestamps acumulados (se existirem)
	var times map[string]string
	if hasTimes {
		times, _ = h.redis.Client.HGetAll(flushCtx, procTimesKey).Result()
	}

	// 3. Batch UPDATE no PostgreSQL com GREATEST
	for siteKey, countStr := range counts {
		cnt, errParse := strconv.ParseInt(countStr, 10, 64)
		if errParse != nil || cnt <= 0 {
			continue
		}

		targetTime := time.Now()
		if hasTimes && times != nil {
			if tsStr, ok := times[siteKey]; ok {
				if sec, errSec := strconv.ParseInt(tsStr, 10, 64); errSec == nil && sec > 0 {
					targetTime = time.Unix(sec, 0)
				}
			}
		}

		_, errUp := h.pg.Pool.Exec(flushCtx, `
			UPDATE site_api_keys
			SET total_events_count = total_events_count + $1,
			    last_used_at = GREATEST(COALESCE(last_used_at, '1970-01-01'::timestamptz), $2)
			WHERE key = $3
		`, cnt, targetTime, siteKey)
		if errUp != nil {
			log.Printf("[Collector Telemetry] Erro no flush da chave %s: %v", siteKey, errUp)
		}
	}

	// 4. Limpa chaves temporárias processadas
	_ = h.redis.Client.Del(flushCtx, procCountsKey).Err()
	if hasTimes {
		_ = h.redis.Client.Del(flushCtx, procTimesKey).Err()
	}
}

// StartKeyInvalidationSubscriber escuta o canal pubsub:key_invalidation e purga chaves revogadas
func (h *Handler) StartKeyInvalidationSubscriber(ctx context.Context) {
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

			pubsub := h.redis.Client.Subscribe(ctx, "pubsub:key_invalidation")
			// Ao inicializar ou reconectar o subscriber: esvazia o cache in-process inteiro
			h.ClearSiteKeysCache()

			ch := pubsub.Channel()
			for msg := range ch {
				var payload struct {
					Action string `json:"action"`
					KeyID  string `json:"key_id"`
					SiteID string `json:"site_id"`
				}
				if err := json.Unmarshal([]byte(msg.Payload), &payload); err == nil {
					if payload.KeyID != "" {
						h.InvalidateKeyByID(payload.KeyID)
					}
					if payload.SiteID != "" {
						h.InvalidateSiteCache(payload.SiteID)
					}
				}
			}

			_ = pubsub.Close()
			// Ao detectar desconexão: esvazia o cache imediatamente para proteção
			h.ClearSiteKeysCache()

			select {
			case <-ctx.Done():
				return
			case <-time.After(1 * time.Second):
			}
		}
	}()
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

// maskUserData mascara dados pessoais sensíveis para exibição segura no DebugView e logs
func maskUserData(ud map[string]interface{}) map[string]interface{} {
	if ud == nil {
		return nil
	}
	masked := make(map[string]interface{})
	for k, v := range ud {
		str, ok := v.(string)
		if !ok || len(str) == 0 {
			masked[k] = v
			continue
		}
		lowerK := strings.ToLower(k)
		if strings.Contains(lowerK, "email") {
			parts := strings.Split(str, "@")
			if len(parts) == 2 && len(parts[0]) > 2 {
				masked[k] = parts[0][:2] + "***@" + parts[1]
			} else {
				masked[k] = "***@***"
			}
		} else if strings.Contains(lowerK, "phone") || strings.Contains(lowerK, "tel") || strings.Contains(lowerK, "cel") || strings.Contains(lowerK, "whats") {
			if len(str) > 4 {
				masked[k] = str[:2] + "*****" + str[len(str)-2:]
			} else {
				masked[k] = "***"
			}
		} else if strings.Contains(lowerK, "name") || strings.Contains(lowerK, "nome") {
			words := strings.Fields(str)
			maskedWords := make([]string, len(words))
			for i, w := range words {
				runes := []rune(w)
				if len(runes) > 1 {
					maskedWords[i] = string(runes[0]) + "***"
				} else {
					maskedWords[i] = w
				}
			}
			masked[k] = strings.Join(maskedWords, " ")
		} else {
			masked[k] = v
		}
	}
	return masked
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
