package integhandler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"tracking-engine/internal/config"
	"tracking-engine/internal/integrations"
	"tracking-engine/internal/storage"
)

type IntegrationsHandler struct {
	cfg          *config.Config
	pg           *storage.PostgresDB
	redis        *storage.RedisClient
	onKeyRevoked func(key string)
}

func NewIntegrationsHandler(cfg *config.Config, pg *storage.PostgresDB, rdb *storage.RedisClient) *IntegrationsHandler {
	return &IntegrationsHandler{
		cfg:   cfg,
		pg:    pg,
		redis: rdb,
	}
}

func (h *IntegrationsHandler) SetOnKeyRevoked(fn func(key string)) {
	h.onKeyRevoked = fn
}


// IntegrationItem representa uma integração de plataforma retornada para a UI
type IntegrationItem struct {
	Platform    string            `json:"platform"`
	IsActive    bool              `json:"is_active"`
	Credentials map[string]string `json:"credentials"`
	HasToken    bool              `json:"has_token"`
	UpdatedAt   *time.Time        `json:"updated_at,omitempty"`
}

// SaveIntegrationRequest payload para criar ou atualizar as credenciais
type SaveIntegrationRequest struct {
	IsActive      bool              `json:"is_active"`
	Credentials   map[string]string `json:"credentials"`
	EventMappings map[string]string `json:"event_mappings,omitempty"`
}

// TestIntegrationRequest payload para disparar o teste sintético
type TestIntegrationRequest struct {
	Credentials map[string]string `json:"credentials"`
}

// TestIntegrationResponse resposta estruturada com o resultado do ping teste
type TestIntegrationResponse struct {
	Success      bool   `json:"success"`
	StatusCode   int    `json:"status_code"`
	Platform     string `json:"platform"`
	LatencyMs    int64  `json:"latency_ms"`
	ResponseBody string `json:"response_body"`
	Error        string `json:"error,omitempty"`
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
		SELECT platform, is_active, credentials, updated_at
		FROM site_integrations
		WHERE site_id = $1
	`, siteUUID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()

	integrationsMap := make(map[string]IntegrationItem)
	for rows.Next() {
		var platform string
		var isActive bool
		var credsBytes []byte
		var updatedAt time.Time

		if err := rows.Scan(&platform, &isActive, &credsBytes, &updatedAt); err == nil {
			var creds map[string]string
			_ = json.Unmarshal(credsBytes, &creds)

			maskedCreds := make(map[string]string)
			hasToken := false

			for k, v := range creds {
				if isSensitiveKey(k) {
					if v != "" {
						hasToken = true
						maskedCreds[k] = maskToken(v)
					} else {
						maskedCreds[k] = ""
					}
				} else {
					maskedCreds[k] = v
				}
			}

			integrationsMap[platform] = IntegrationItem{
				Platform:    platform,
				IsActive:    isActive,
				Credentials: maskedCreds,
				HasToken:    hasToken,
				UpdatedAt:   &updatedAt,
			}
		}
	}

	// Garante que as 3 principais plataformas estejam sempre no objeto de retorno
	platforms := []string{"meta_capi", "google_ads", "ga4"}
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
	if platform != "meta_capi" && platform != "google_ads" && platform != "ga4" && platform != "webhook" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "plataforma não suportada"})
	}

	var req SaveIntegrationRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "dados inválidos"})
	}

	if req.Credentials == nil {
		req.Credentials = make(map[string]string)
	}

	if h.pg == nil || h.pg.Pool == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "banco de dados indisponível"})
	}

	// Busca credenciais existentes para não sobrescrever tokens se o usuário não os reenviou
	var existingCredsJSON []byte
	row := h.pg.Pool.QueryRow(c.Context(), `
		SELECT credentials FROM site_integrations WHERE site_id = $1 AND platform = $2
	`, siteUUID, platform)

	existingCreds := make(map[string]string)
	if err := row.Scan(&existingCredsJSON); err == nil && len(existingCredsJSON) > 0 {
		_ = json.Unmarshal(existingCredsJSON, &existingCreds)
	}

	// Mescla credenciais preservando tokens não alterados (que vêm mascarados ou vazios)
	finalCreds := make(map[string]string)
	for k, v := range existingCreds {
		finalCreds[k] = v
	}

	for k, v := range req.Credentials {
		trimmed := strings.TrimSpace(v)
		if isSensitiveKey(k) && (strings.Contains(trimmed, "••") || trimmed == "") {
			// Mantém token existente caso o usuário tenha enviado a máscara visual
			continue
		}
		finalCreds[k] = trimmed
	}

	credsJSON, err := json.Marshal(finalCreds)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "erro ao serializar credenciais"})
	}

	eventMappingsJSON, _ := json.Marshal(req.EventMappings)
	if req.EventMappings == nil {
		eventMappingsJSON = []byte("{}")
	}

	query := `
		INSERT INTO site_integrations (site_id, platform, is_active, credentials, event_mappings, updated_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (site_id, platform)
		DO UPDATE SET 
			is_active = EXCLUDED.is_active,
			credentials = EXCLUDED.credentials,
			event_mappings = EXCLUDED.event_mappings,
			updated_at = now()
	`

	_, err = h.pg.Pool.Exec(c.Context(), query, siteUUID, platform, req.IsActive, credsJSON, eventMappingsJSON)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "falha ao salvar integração: " + err.Error()})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"message":  "Integração salva com sucesso",
		"platform": platform,
		"active":   req.IsActive,
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
		endpointURL := strings.TrimSpace(creds["endpoint_url"])
		token := strings.TrimSpace(creds["api_token"])
		action := strings.TrimSpace(creds["conversion_action"])

		if endpointURL == "" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "endpoint_url é obrigatório para testar o Google Ads",
			})
		}

		gads := integrations.NewGoogleAds(client)
		respBody, statusCode, testErr = gads.TestPing(ctx, endpointURL, token, action)

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

	return c.Status(fiber.StatusOK).JSON(TestIntegrationResponse{
		Success:      success,
		StatusCode:   statusCode,
		Platform:     platform,
		LatencyMs:    latency,
		ResponseBody: bodyStr,
		Error:        errMsg,
	})
}

func isSensitiveKey(k string) bool {
	lower := strings.ToLower(k)
	return strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || strings.Contains(lower, "key")
}

func maskToken(t string) string {
	if len(t) <= 8 {
		return "••••••••"
	}
	prefix := t[:4]
	suffix := t[len(t)-4:]
	return fmt.Sprintf("%s••••••••%s", prefix, suffix)
}
