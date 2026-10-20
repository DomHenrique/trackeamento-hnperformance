package integhandler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"tracking-engine/internal/config"
	"tracking-engine/internal/crypto"
	"tracking-engine/internal/integrations"
	"tracking-engine/internal/storage"
)

const defaultDevKeyHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type IntegrationsHandler struct {
	cfg          *config.Config
	pg           *storage.PostgresDB
	redis        *storage.RedisClient
	keyRing      *crypto.KeyRing
	onKeyRevoked func(key string)
}

func NewIntegrationsHandler(cfg *config.Config, pg *storage.PostgresDB, rdb *storage.RedisClient) *IntegrationsHandler {
	h := &IntegrationsHandler{
		cfg:   cfg,
		pg:    pg,
		redis: rdb,
	}
	return h
}

func (h *IntegrationsHandler) SetKeyRing(kr *crypto.KeyRing) {
	h.keyRing = kr
}

func (h *IntegrationsHandler) getKeyRing() *crypto.KeyRing {
	if h.keyRing != nil {
		return h.keyRing
	}
	if h.cfg != nil && h.cfg.KeyRing != nil {
		return h.cfg.KeyRing
	}
	// Fallback determinístico para desenvolvimento e testes
	kr, _ := crypto.NewKeyRingFromJSON(`{"dev": "`+defaultDevKeyHex+`"}`, "dev")
	return kr
}

func (h *IntegrationsHandler) SetOnKeyRevoked(fn func(key string)) {
	h.onKeyRevoked = fn
}


// IntegrationItem representa uma integração de plataforma retornada para a UI
type IntegrationItem struct {
	Platform      string            `json:"platform"`
	IsActive      bool              `json:"is_active"`
	Credentials   map[string]string `json:"credentials"`
	HasToken      bool              `json:"has_token"`
	EventMappings map[string]string `json:"event_mappings,omitempty"`
	UpdatedAt     *time.Time        `json:"updated_at,omitempty"`
}

// PlatformAllowedCredentials define a whitelist estrita de campos de credenciais por plataforma
var PlatformAllowedCredentials = map[string]map[string]bool{
	"meta_capi": {
		"pixel_id":        true,
		"access_token":    true,
		"test_event_code": true,
	},
	"google_ads": {
		"endpoint_url":         true,
		"api_token":            true,
		"conversion_action":    true,
		"client_id":            true,
		"client_secret":        true,
		"refresh_token":        true,
		"operating_account_id": true,
		"login_account_id":     true,
		"conversion_action_id": true,
	},
	"ga4": {
		"measurement_id": true,
		"api_secret":     true,
	},
	"linkedin_capi": {
		"access_token":        true,
		"conversion_rule_id": true,
	},
	"webhook": {
		"url":          true,
		"webhook_url":  true,
		"secret_token": true,
		"secret":       true,
		"headers":      true,
	},
}

// SaveIntegrationRequest payload para criar ou atualizar as credenciais
type SaveIntegrationRequest struct {
	IsActive      *bool             `json:"is_active"`
	Credentials   map[string]string `json:"credentials"`
	DeleteKeys    []string          `json:"delete_keys"`
	EventMappings map[string]string `json:"event_mappings,omitempty"`
}

// TestIntegrationRequest payload para disparar o teste sintético
type TestIntegrationRequest struct {
	Credentials map[string]string `json:"credentials"`
}

// TestIntegrationResponse resposta estruturada com o resultado do ping teste
type TestIntegrationResponse struct {
	Success           bool   `json:"success"`
	StatusCode        int    `json:"status_code"`
	Platform          string `json:"platform"`
	LatencyMs         int64  `json:"latency_ms"`
	ResponseBody      string `json:"response_body"`
	Error             string `json:"error,omitempty"`
	FriendlyTitle     string `json:"friendly_title,omitempty"`
	FriendlyMessage   string `json:"friendly_message,omitempty"`
	ActionAdvice      string `json:"action_advice,omitempty"`
	PropagationNotice string `json:"propagation_notice,omitempty"`
}

// HandleGetIntegrations retorna as integrações de um site com credenciais sensíveis mascaradas
func (h *IntegrationsHandler) HandleGetIntegrations(c *fiber.Ctx) error {
	siteIDStr := strings.TrimSpace(c.Params("site_id"))
	siteUUID, err := uuid.Parse(siteIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "site_id inválido"})
	}

	if h.pg == nil || h.pg.Pool == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "banco de dados indisponível"})
	}

	rows, err := h.pg.Pool.Query(c.Context(), `
		SELECT platform, is_active, credentials, COALESCE(event_mappings, '{}'::jsonb), updated_at
		FROM site_integrations
		WHERE site_id = $1
	`, siteUUID)
	if err != nil {
		log.Printf("[Integrations] Falha ao consultar integrações do site %s: %v", siteUUID, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "falha ao carregar integrações do site"})
	}
	defer rows.Close()

	integrationsMap := make(map[string]IntegrationItem)
	for rows.Next() {
		var platform string
		var isActive bool
		var credsBytes []byte
		var eventMappingsBytes []byte
		var updatedAt time.Time

		if err := rows.Scan(&platform, &isActive, &credsBytes, &eventMappingsBytes, &updatedAt); err != nil {
			log.Printf("[Integrations] Erro no scan de integração para site %s: %v", siteUUID, err)
			continue
		}

		var creds map[string]string
		if err := json.Unmarshal(credsBytes, &creds); err != nil {
			log.Printf("[Integrations] Erro ao deserializar credenciais do site %s plataforma %s: %v", siteUUID, platform, err)
			creds = make(map[string]string)
		}

		var eventMappings map[string]string
		if len(eventMappingsBytes) > 0 {
			if err := json.Unmarshal(eventMappingsBytes, &eventMappings); err != nil {
				log.Printf("[Integrations] Erro ao deserializar event_mappings do site %s plataforma %s: %v", siteUUID, platform, err)
			}
		}

		maskedCreds := make(map[string]string)
		hasToken := false
		kr := h.getKeyRing()

		for k, v := range creds {
			if kr != nil && crypto.IsEncrypted(v) {
				decrypted, err := kr.DecryptField(v, siteUUID.String(), platform, k)
				if err != nil {
					log.Printf("[Integrations] Falha ao decifrar credencial %s para site %s: %v", k, siteUUID, err)
				} else {
					v = decrypted
				}
			}

			if isSensitiveKey(k) {
				if v != "" {
					hasToken = true
					maskedCreds[k] = maskToken(v)
				} else {
					maskedCreds[k] = ""
				}
			} else if isURLKey(k) {
				maskedCreds[k] = maskSensitiveQueryParams(v)
			} else {
				maskedCreds[k] = v
			}
		}

		integrationsMap[platform] = IntegrationItem{
			Platform:      platform,
			IsActive:      isActive,
			Credentials:   maskedCreds,
			HasToken:      hasToken,
			EventMappings: eventMappings,
			UpdatedAt:     &updatedAt,
		}
	}

	if err := rows.Err(); err != nil {
		log.Printf("[Integrations] Erro ao iterar linhas de integrações para site %s: %v", siteUUID, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "falha ao iterar integrações"})
	}

	// Garante que as principais plataformas estejam sempre no objeto de retorno
	platforms := []string{"meta_capi", "google_ads", "ga4", "linkedin_capi"}
	response := make([]IntegrationItem, 0, len(platforms))

	for _, p := range platforms {
		if item, exists := integrationsMap[p]; exists {
			response = append(response, item)
		} else {
			response = append(response, IntegrationItem{
				Platform:    p,
				IsActive:    false,
				Credentials: make(map[string]string),
				HasToken:    false,
			})
		}
	}

	return c.Status(fiber.StatusOK).JSON(response)
}

// HandleSaveIntegration cria ou atualiza as credenciais de uma plataforma para um site
func (h *IntegrationsHandler) HandleSaveIntegration(c *fiber.Ctx) error {
	siteIDStr := strings.TrimSpace(c.Params("site_id"))
	siteUUID, err := uuid.Parse(siteIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "site_id inválido"})
	}

	platform := strings.ToLower(strings.TrimSpace(c.Params("platform")))
	if platform != "meta_capi" && platform != "google_ads" && platform != "ga4" && platform != "linkedin_capi" && platform != "webhook" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "plataforma não suportada"})
	}

	var req SaveIntegrationRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "dados inválidos"})
	}

	if req.Credentials == nil {
		req.Credentials = make(map[string]string)
	}

	// Validação de whitelist de chaves em credentials
	allowed, ok := PlatformAllowedCredentials[platform]
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "plataforma não suportada"})
	}

	for k := range req.Credentials {
		if !allowed[k] {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": fmt.Sprintf("chave %q não é suportada para a plataforma %s", k, platform),
			})
		}
	}

	// Validação de delete_keys e detecção de conflitos
	for _, delKey := range req.DeleteKeys {
		delKey = strings.TrimSpace(delKey)
		if delKey == "" {
			continue
		}
		if !allowed[delKey] {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": fmt.Sprintf("chave em delete_keys %q não é suportada para a plataforma %s", delKey, platform),
			})
		}
		if _, exists := req.Credentials[delKey]; exists {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": fmt.Sprintf("conflito de chave: %q não pode constar em credentials e delete_keys simultaneamente", delKey),
			})
		}
	}

	// Validação preventiva contra SSRF para plataformas que configuram URLs de destino
	env := "production"
	if h.cfg != nil && h.cfg.Env != "" {
		env = h.cfg.Env
	}

	if platform == "google_ads" {
		if endpoint := strings.TrimSpace(req.Credentials["endpoint_url"]); endpoint != "" {
			if err := integrations.ValidateOutboundURL(endpoint, env); err != nil {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
					"error": fmt.Sprintf("endpoint_url invalido ou restrito: %v", err),
				})
			}
		}
	} else if platform == "webhook" {
		webhookURL := strings.TrimSpace(req.Credentials["url"])
		if webhookURL == "" {
			webhookURL = strings.TrimSpace(req.Credentials["webhook_url"])
		}
		if webhookURL != "" {
			if err := integrations.ValidateOutboundURL(webhookURL, env); err != nil {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
					"error": fmt.Sprintf("url de webhook invalida ou restrita: %v", err),
				})
			}
		}
	}

	if h.pg == nil || h.pg.Pool == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "banco de dados indisponível"})
	}

	kr := h.getKeyRing()

	// Prepara mapa de credenciais que serão inseridas/atualizadas
	newCredsToStore := make(map[string]string)
	for k, v := range req.Credentials {
		trimmed := strings.TrimSpace(v)
		if isSensitiveKey(k) {
			if strings.Contains(trimmed, "••") || trimmed == "" {
				// Ignora máscara visual ou valor vazio para que o merge do Postgres preserve o token existente
				continue
			}
			// Cifra novo token sensível com AES-256-GCM e AAD contextual
			if kr != nil {
				encryptedVal, err := kr.EncryptField(trimmed, siteUUID.String(), platform, k)
				if err != nil {
					log.Printf("[Integrations] Falha ao criptografar campo %s para site %s: %v", k, siteUUID, err)
					return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "falha ao proteger credenciais"})
				}
				newCredsToStore[k] = encryptedVal
			} else {
				newCredsToStore[k] = trimmed
			}
		} else {
			// Campos não sensíveis (ex: pixel_id, measurement_id, endpoint_url) salvos em texto claro
			newCredsToStore[k] = trimmed
		}
	}

	credsJSON, err := json.Marshal(newCredsToStore)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "erro ao serializar credenciais"})
	}

	eventMappingsJSON, _ := json.Marshal(req.EventMappings)
	if req.EventMappings == nil {
		eventMappingsJSON = []byte("{}")
	}

	// Garante que deleteKeys seja sempre um slice instanciado (nunca nil) para evitar o comportamento NULL no Postgres
	deleteKeys := req.DeleteKeys
	if deleteKeys == nil {
		deleteKeys = []string{}
	}

	query := `
		INSERT INTO site_integrations (site_id, platform, is_active, credentials, event_mappings, updated_at)
		VALUES ($1, $2, COALESCE($3, true), $4::jsonb, $5::jsonb, now())
		ON CONFLICT (site_id, platform)
		DO UPDATE SET 
			is_active = COALESCE(EXCLUDED.is_active, site_integrations.is_active),
			credentials = (COALESCE(site_integrations.credentials, '{}'::jsonb) - COALESCE($6::text[], '{}')) || EXCLUDED.credentials,
			event_mappings = CASE 
				WHEN EXCLUDED.event_mappings = '{}'::jsonb AND site_integrations.event_mappings <> '{}'::jsonb 
				THEN site_integrations.event_mappings 
				ELSE EXCLUDED.event_mappings 
			END,
			updated_at = now()
	`

	_, err = h.pg.Pool.Exec(c.Context(), query, siteUUID, platform, req.IsActive, credsJSON, eventMappingsJSON, deleteKeys)
	if err != nil {
		log.Printf("[Integrations] Falha ao salvar integração para site %s plataforma %s: %v", siteUUID, platform, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "falha ao salvar integração"})
	}

	isActiveResp := true
	if req.IsActive != nil {
		isActiveResp = *req.IsActive
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"message":  "Integração salva com sucesso",
		"platform": platform,
		"active":   isActiveResp,
	})
}

// HandleTestIntegration dispara um ping de teste síncrono contra a API oficial da plataforma selecionada
func (h *IntegrationsHandler) HandleTestIntegration(c *fiber.Ctx) error {
	siteIDStr := strings.TrimSpace(c.Params("site_id"))
	siteUUID, err := uuid.Parse(siteIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "site_id inválido"})
	}

	platform := strings.ToLower(strings.TrimSpace(c.Params("platform")))

	var req TestIntegrationRequest
	_ = c.BodyParser(&req)

	// Se não forneceu credenciais completas no body do teste, busca do banco de dados
	creds := req.Credentials
	if creds == nil {
		creds = make(map[string]string)
	}

	if h.pg != nil && h.pg.Pool != nil {
		var dbCredsJSON []byte
		row := h.pg.Pool.QueryRow(c.Context(), `
			SELECT credentials FROM site_integrations WHERE site_id = $1 AND platform = $2
		`, siteUUID, platform)
		if err := row.Scan(&dbCredsJSON); err == nil && len(dbCredsJSON) > 0 {
			var dbCreds map[string]string
			if err := json.Unmarshal(dbCredsJSON, &dbCreds); err == nil {
				for k, v := range dbCreds {
					if currentVal, exists := creds[k]; !exists || strings.TrimSpace(currentVal) == "" || strings.Contains(currentVal, "••") {
						creds[k] = v
					}
				}
			}
		}
	}

	// Decifra transparentemente qualquer credencial cifrada antes de invocar a chamada de teste
	kr := h.getKeyRing()
	if kr != nil {
		for k, v := range creds {
			if crypto.IsEncrypted(v) {
				decrypted, err := kr.DecryptField(v, siteUUID.String(), platform, k)
				if err != nil {
					log.Printf("[Integrations] Falha ao decifrar credencial %s para teste no site %s: %v", k, siteUUID, err)
				} else {
					creds[k] = decrypted
				}
			}
		}
	}

	client := integrations.NewHTTPClient(10 * time.Second)
	startTime := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var respBody []byte
	var statusCode int
	var testErr error

	switch platform {
	case "meta_capi":
		pixelID := strings.TrimSpace(creds["pixel_id"])
		token := strings.TrimSpace(creds["access_token"])
		testCode := strings.TrimSpace(creds["test_event_code"])

		if pixelID == "" || token == "" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "pixel_id e access_token são obrigatórios para testar a Meta CAPI",
			})
		}

		meta := integrations.NewMetaCAPI(client)
		respBody, statusCode, testErr = meta.TestPing(ctx, pixelID, token, testCode)

	case "ga4":
		measurementID := strings.TrimSpace(creds["measurement_id"])
		apiSecret := strings.TrimSpace(creds["api_secret"])

		if measurementID == "" || apiSecret == "" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "measurement_id e api_secret são obrigatórios para testar o GA4",
			})
		}

		ga4 := integrations.NewGA4MP(client)
		respBody, statusCode, testErr = ga4.TestPing(ctx, measurementID, apiSecret)

	case "google_ads":
		// 1. Se possuir credenciais Google Data Manager (OAuth2)
		if strings.TrimSpace(creds["client_id"]) != "" && strings.TrimSpace(creds["refresh_token"]) != "" {
			dmCfg := integrations.GoogleDataManagerConfig{
				ClientID:           strings.TrimSpace(creds["client_id"]),
				ClientSecret:       strings.TrimSpace(creds["client_secret"]),
				RefreshToken:       strings.TrimSpace(creds["refresh_token"]),
				OperatingAccountID: strings.TrimSpace(creds["operating_account_id"]),
				LoginAccountID:     strings.TrimSpace(creds["login_account_id"]),
				ConversionActionID: strings.TrimSpace(creds["conversion_action_id"]),
			}
			if dmCfg.OperatingAccountID == "" {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
					"error": "operating_account_id é obrigatório para testar o Google Data Manager",
				})
			}
			if dmCfg.ConversionActionID == "" {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
					"error": "conversion_action_id é obrigatório para testar o Google Data Manager",
				})
			}

			dm := integrations.NewGoogleDataManager(client)
			respBody, statusCode, testErr = dm.TestPing(ctx, dmCfg)
		} else {
			// 2. Fallback para webhook legado
			endpointURL := strings.TrimSpace(creds["endpoint_url"])
			token := strings.TrimSpace(creds["api_token"])
			action := strings.TrimSpace(creds["conversion_action"])

			if endpointURL == "" {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
					"error": "endpoint_url ou credenciais OAuth2 do Data Manager são obrigatórios para testar o Google Ads",
				})
			}

			env := "production"
			if h.cfg != nil && h.cfg.Env != "" {
				env = h.cfg.Env
			}
			if err := integrations.ValidateOutboundURL(endpointURL, env); err != nil {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
					"error": fmt.Sprintf("endpoint_url invalido ou restrito: %v", err),
				})
			}

			gads := integrations.NewGoogleAds(client)
			respBody, statusCode, testErr = gads.TestPing(ctx, endpointURL, token, action)
		}

	case "linkedin_capi":
		token := strings.TrimSpace(creds["access_token"])
		ruleID := strings.TrimSpace(creds["conversion_rule_id"])

		if token == "" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "access_token é obrigatório para testar a LinkedIn Conversions API",
			})
		}

		li := integrations.NewLinkedInCAPI(client)
		respBody, statusCode, testErr = li.TestPing(ctx, token, ruleID)

	default:
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "plataforma desconhecida para teste"})
	}

	latency := time.Since(startTime).Milliseconds()

	success := (statusCode >= 200 && statusCode < 300)
	errMsg := ""
	if testErr != nil {
		errMsg = testErr.Error()
	}

	bodyStr := string(respBody)
	if bodyStr == "" && success {
		bodyStr = `{"status": "ok", "message": "Evento aceito pela API com sucesso"}`
	}

	var friendlyTitle, friendlyMsg, actionAdvice, propagationNotice string
	if platform == "linkedin_capi" {
		diag := integrations.InterpretLinkedInError(statusCode, respBody)
		friendlyTitle = diag.FriendlyTitle
		friendlyMsg = diag.FriendlyMessage
		actionAdvice = diag.ActionAdvice
		propagationNotice = diag.PropagationNotice
	}

	return c.Status(fiber.StatusOK).JSON(TestIntegrationResponse{
		Success:           success,
		StatusCode:        statusCode,
		Platform:          platform,
		LatencyMs:         latency,
		ResponseBody:      bodyStr,
		Error:             errMsg,
		FriendlyTitle:     friendlyTitle,
		FriendlyMessage:   friendlyMsg,
		ActionAdvice:      actionAdvice,
		PropagationNotice: propagationNotice,
	})
}

func isSensitiveKey(k string) bool {
	lower := strings.ToLower(k)
	return strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || strings.Contains(lower, "key")
}

func isURLKey(k string) bool {
	lower := strings.ToLower(k)
	return strings.Contains(lower, "url") || strings.Contains(lower, "endpoint")
}

func maskSensitiveQueryParams(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.RawQuery == "" {
		return rawURL
	}
	q := u.Query()
	sensitiveParams := []string{"token", "key", "secret", "sig", "signature", "api_key", "apikey", "auth"}
	modified := false
	for _, param := range sensitiveParams {
		for queryKey := range q {
			if strings.EqualFold(queryKey, param) {
				val := q.Get(queryKey)
				if val != "" {
					q.Set(queryKey, maskToken(val))
					modified = true
				}
			}
		}
	}
	if modified {
		u.RawQuery = q.Encode()
		return u.String()
	}
	return rawURL
}

func maskToken(t string) string {
	if len(t) < 16 {
		return "••••••••"
	}
	suffix := t[len(t)-4:]
	return fmt.Sprintf("••••••••%s", suffix)
}

// HandleGetRoutingSummary retorna o sumário diário de telemetria de roteamento para um site ou global
func (h *IntegrationsHandler) HandleGetRoutingSummary(c *fiber.Ctx) error {
	siteID := strings.TrimSpace(c.Params("site_id"))
	if siteID == "" || siteID == "all" || siteID == "global" {
		siteID = "global"
	} else {
		if _, err := uuid.Parse(siteID); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "site_id inválido"})
		}
	}

	if h.redis == nil {
		return c.Status(fiber.StatusOK).JSON(fiber.Map{
			"total":           0,
			"success":         0,
			"error":           0,
			"blocked_consent": 0,
			"blocked_gpc":     0,
			"success_rate":    "100.0",
			"destinations":    map[string]interface{}{},
		})
	}

	summary, err := h.redis.GetRoutingSummary(c.Context(), siteID)
	if err != nil {
		log.Printf("[Integrations] Falha ao obter sumário de roteamento para site %s: %v", siteID, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "falha ao obter métricas de roteamento"})
	}

	return c.Status(fiber.StatusOK).JSON(summary)
}

// HandleGetRoutingRecent retorna o buffer circular das últimas decisões de roteamento para um site ou global
func (h *IntegrationsHandler) HandleGetRoutingRecent(c *fiber.Ctx) error {
	siteID := strings.TrimSpace(c.Params("site_id"))
	if siteID == "" || siteID == "all" || siteID == "global" {
		siteID = "global"
	} else {
		if _, err := uuid.Parse(siteID); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "site_id inválido"})
		}
	}

	limit := int64(30)
	if l := c.QueryInt("limit", 0); l > 0 && l <= 50 {
		limit = int64(l)
	}

	if h.redis == nil {
		return c.Status(fiber.StatusOK).JSON([]storage.RoutingDecisionRecord{})
	}

	recent, err := h.redis.GetRecentRoutingDecisions(c.Context(), siteID, limit)
	if err != nil {
		log.Printf("[Integrations] Falha ao obter decisões recentes de roteamento para site %s: %v", siteID, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "falha ao carregar decisões recentes"})
	}

	return c.Status(fiber.StatusOK).JSON(recent)
}

