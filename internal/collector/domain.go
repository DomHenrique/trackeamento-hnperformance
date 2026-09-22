package collector

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"tracking-engine/internal/prefixedid"
)

type SiteMetadata struct {
	ID             string
	AllowedDomains []string
}

// ExtractOriginDomain extrai o domínio limpo da requisição.
// Prioriza os cabeçalhos padrão de navegador Origin e Referer.
// Se ambos estiverem ausentes (ex: políticas restritivas no-referrer, sendBeacon ou modo preview do GTM)
// ou se for chamada server-side autenticada (isServerAuth), realiza fallback seguro para a URL do payload (req.URL / req.PageURL / req.Referrer).
// O domínio extraído ainda é estritamente validado contra a whitelist de domínios permitidos do site.
func ExtractOriginDomain(c *fiber.Ctx, req *EventRequest, isServerAuth bool) string {
	// 1. Tenta header Origin (enviado por fetch/XHR do navegador)
	origin := strings.TrimSpace(c.Get("Origin"))
	if origin != "" {
		if d := cleanHost(origin); d != "" {
			return d
		}
	}

	// 2. Tenta header Referer
	referer := strings.TrimSpace(c.Get("Referer"))
	if referer != "" {
		if d := cleanHost(referer); d != "" {
			return d
		}
	}

	// 3. Fallback: se Origin e Referer estiverem ausentes ou se for chamada server-side autenticada
	if req != nil {
		pageURL := req.URL
		if pageURL == "" {
			pageURL = req.PageURL
		}
		if pageURL != "" {
			if d := cleanHost(pageURL); d != "" {
				return d
			}
		}

		if req.Referrer != "" {
			if d := cleanHost(req.Referrer); d != "" {
				return d
			}
		}
	}

	return ""
}

// cleanHost normaliza uma URL/origem para apenas o host em minúsculas sem porta
func cleanHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	// Se não tiver protocolo, adiciona https:// temporário para url.Parse analisar corretamente
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}

	return strings.ToLower(strings.TrimSpace(u.Hostname()))
}

// IsDomainAllowed verifica se o host está autorizado, aceitando subdomínios automaticamente
func IsDomainAllowed(host string, allowedDomains []string, isDev bool) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}

	// Em desenvolvimento, libera localhost e 127.0.0.1
	if isDev && (host == "localhost" || host == "127.0.0.1" || strings.HasSuffix(host, ".local")) {
		return true
	}

	for _, allowed := range allowedDomains {
		allowed = strings.ToLower(strings.TrimSpace(allowed))
		if allowed == "" {
			continue
		}

		// Correspondência exata (ex: "spspower.com.br" == "spspower.com.br")
		if host == allowed {
			return true
		}

		// Subdomínio legítimo (ex: "lp.spspower.com.br" termina com ".spspower.com.br")
		if strings.HasSuffix(host, "."+allowed) {
			return true
		}
	}

	return false
}

// RecordDomainAlert registra assincronamente a tentativa de acesso não autorizada
func (h *Handler) RecordDomainAlert(siteID, domain, ip, ua, rawURL string) {
	if h.pg == nil || h.pg.Pool == nil || siteID == "" || domain == "" {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		query := `
			INSERT INTO site_domain_alerts (site_id, unauthorized_domain, attempts_count, first_attempt_at, last_attempt_at, last_ip, last_user_agent, last_raw_url)
			VALUES ($1, $2, 1, now(), now(), $3, $4, $5)
			ON CONFLICT (site_id, unauthorized_domain) DO UPDATE SET
				attempts_count = site_domain_alerts.attempts_count + 1,
				last_attempt_at = now(),
				last_ip = EXCLUDED.last_ip,
				last_user_agent = EXCLUDED.last_user_agent,
				last_raw_url = EXCLUDED.last_raw_url
		`
		_, _ = h.pg.Pool.Exec(ctx, query, siteID, domain, ip, ua, rawURL)
	}()
}

type DomainAlertItem struct {
	ID                 string    `json:"id"`
	SiteID             string    `json:"site_id"`
	SiteName           string    `json:"site_name,omitempty"`
	UnauthorizedDomain string    `json:"unauthorized_domain"`
	AttemptsCount      int       `json:"attempts_count"`
	FirstAttemptAt     time.Time `json:"first_attempt_at"`
	LastAttemptAt      time.Time `json:"last_attempt_at"`
	LastIP             string    `json:"last_ip"`
	LastUserAgent      string    `json:"last_user_agent"`
	LastRawURL         string    `json:"last_raw_url"`
}

// HandleListAlerts retorna a lista de domínios não autorizados bloqueados
func (h *Handler) HandleListAlerts(c *fiber.Ctx) error {
	if h.pg == nil || h.pg.Pool == nil {
		return c.Status(fiber.StatusOK).JSON([]DomainAlertItem{})
	}

	siteKey := prefixedid.SanitizeKey(c.Query("site_key"))
	var query string
	var args []interface{}

	if siteKey != "" {
		meta, ok := h.validateSiteKey(c.Context(), siteKey)
		if !ok {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "site_key invalida"})
		}
		query = `
			SELECT a.id::text, a.site_id::text, s.name, a.unauthorized_domain, a.attempts_count,
			       a.first_attempt_at, a.last_attempt_at, COALESCE(a.last_ip, ''), COALESCE(a.last_user_agent, ''), COALESCE(a.last_raw_url, '')
			FROM site_domain_alerts a
			JOIN sites s ON s.id = a.site_id
			WHERE a.site_id = $1
			ORDER BY a.last_attempt_at DESC
			LIMIT 100
		`
		args = append(args, meta.ID)
	} else {
		query = `
			SELECT a.id::text, a.site_id::text, s.name, a.unauthorized_domain, a.attempts_count,
			       a.first_attempt_at, a.last_attempt_at, COALESCE(a.last_ip, ''), COALESCE(a.last_user_agent, ''), COALESCE(a.last_raw_url, '')
			FROM site_domain_alerts a
			JOIN sites s ON s.id = a.site_id
			ORDER BY a.last_attempt_at DESC
			LIMIT 100
		`
	}

	rows, err := h.pg.Pool.Query(c.Context(), query, args...)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()

	alerts := make([]DomainAlertItem, 0)
	for rows.Next() {
		var item DomainAlertItem
		if err := rows.Scan(
			&item.ID, &item.SiteID, &item.SiteName, &item.UnauthorizedDomain, &item.AttemptsCount,
			&item.FirstAttemptAt, &item.LastAttemptAt, &item.LastIP, &item.LastUserAgent, &item.LastRawURL,
		); err == nil {
			alerts = append(alerts, item)
		}
	}

	return c.Status(fiber.StatusOK).JSON(alerts)
}

// InvalidateSiteCache invalida a entrada em memória correspondente ao siteID
func (h *Handler) InvalidateSiteCache(siteID string) {
	h.siteKeys.Range(func(key, val interface{}) bool {
		if meta, ok := val.(*SiteMetadata); ok && meta.ID == siteID {
			h.siteKeys.Delete(key)
		}
		return true
	})
}

// InvalidateKey invalida uma chave de API específica do cache em memória
func (h *Handler) InvalidateKey(key string) {
	h.siteKeys.Delete(key)
}


type SiteItem struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Domain         string   `json:"domain"`
	AllowedDomains []string `json:"allowed_domains"`
}

// HandleListSites retorna a lista de sites ativos para o dropdown da UI (sem expor credenciais/api_key)
func (h *Handler) HandleListSites(c *fiber.Ctx) error {
	if h.pg == nil || h.pg.Pool == nil {
		return c.Status(fiber.StatusOK).JSON([]SiteItem{})
	}

	rows, err := h.pg.Pool.Query(c.Context(), `
		SELECT s.id::text, s.name, s.domain,
		       COALESCE(ARRAY_AGG(d.domain) FILTER (WHERE d.domain IS NOT NULL AND d.is_active = true), '{}') AS allowed_domains
		FROM sites s
		LEFT JOIN site_allowed_domains d ON d.site_id = s.id
		WHERE s.is_active = true
		GROUP BY s.id, s.name, s.domain
		ORDER BY s.name ASC
	`)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()

	sites := make([]SiteItem, 0)
	for rows.Next() {
		var s SiteItem
		if err := rows.Scan(&s.ID, &s.Name, &s.Domain, &s.AllowedDomains); err == nil {
			sites = append(sites, s)
		}
	}
	return c.Status(fiber.StatusOK).JSON(sites)
}

// HandleCreateSite registra um novo site gerando automaticamente uma chave padronizada hn_site_
func (h *Handler) HandleCreateSite(c *fiber.Ctx) error {
	var req struct {
		Name     string `json:"name"`
		Domain   string `json:"domain"`
		ClientID string `json:"client_id,omitempty"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "dados invalidos"})
	}

	name := strings.TrimSpace(req.Name)
	domain := cleanHost(req.Domain)
	if name == "" || domain == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "name e domain sao obrigatorios"})
	}

	if h.pg == nil || h.pg.Pool == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "banco de dados indisponivel"})
	}

	clientID := strings.TrimSpace(req.ClientID)
	if clientID == "" {
		_ = h.pg.Pool.QueryRow(c.Context(), "SELECT id::text FROM clients ORDER BY created_at ASC LIMIT 1").Scan(&clientID)
	}
	if clientID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "client_id obrigatorio"})
	}

	newSiteKey := prefixedid.GenerateSiteKey()
	var newSiteID string

	err := h.pg.Pool.QueryRow(c.Context(), `
		INSERT INTO sites (client_id, domain, name, api_key, is_active)
		VALUES ($1, $2, $3, $4, true)
		RETURNING id::text
	`, clientID, domain, name, newSiteKey).Scan(&newSiteID)

	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": fmt.Sprintf("erro ao cadastrar site: %v", err)})
	}

	// Adiciona o proprio dominio como permitido por padrao
	_, _ = h.pg.Pool.Exec(c.Context(), `
		INSERT INTO site_allowed_domains (site_id, domain, is_active)
		VALUES ($1, $2, true)
		ON CONFLICT (site_id, domain) DO NOTHING
	`, newSiteID, domain)

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"id":       newSiteID,
		"name":     name,
		"domain":   domain,
		"site_key": newSiteKey,
	})
}

type AllowedDomainItem struct {
	ID        string    `json:"id"`
	SiteID    string    `json:"site_id"`
	SiteName  string    `json:"site_name,omitempty"`
	Domain    string    `json:"domain"`
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
}

// HandleListDomains retorna os domínios permitidos
func (h *Handler) HandleListDomains(c *fiber.Ctx) error {
	if h.pg == nil || h.pg.Pool == nil {
		return c.Status(fiber.StatusOK).JSON([]AllowedDomainItem{})
	}

	siteID := strings.TrimSpace(c.Query("site_id"))
	var query string
	var args []interface{}

	if siteID != "" {
		query = `
			SELECT d.id::text, d.site_id::text, s.name, d.domain, d.is_active, d.created_at
			FROM site_allowed_domains d
			JOIN sites s ON s.id = d.site_id
			WHERE d.site_id = $1
			ORDER BY d.created_at DESC
		`
		args = append(args, siteID)
	} else {
		query = `
			SELECT d.id::text, d.site_id::text, s.name, d.domain, d.is_active, d.created_at
			FROM site_allowed_domains d
			JOIN sites s ON s.id = d.site_id
			ORDER BY d.created_at DESC
		`
	}

	rows, err := h.pg.Pool.Query(c.Context(), query, args...)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()

	domains := make([]AllowedDomainItem, 0)
	for rows.Next() {
		var item AllowedDomainItem
		if err := rows.Scan(&item.ID, &item.SiteID, &item.SiteName, &item.Domain, &item.IsActive, &item.CreatedAt); err == nil {
			domains = append(domains, item)
		}
	}

	return c.Status(fiber.StatusOK).JSON(domains)
}

// HandleAddDomain cadastra um novo domínio autorizado para um site
func (h *Handler) HandleAddDomain(c *fiber.Ctx) error {
	var req struct {
		SiteID string `json:"site_id"`
		Domain string `json:"domain"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "dados invalidos"})
	}

	siteID := strings.TrimSpace(req.SiteID)
	domain := cleanHost(req.Domain)
	if siteID == "" || domain == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "site_id e domain sao obrigatorios"})
	}

	if h.pg == nil || h.pg.Pool == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "banco de dados indisponivel"})
	}

	var newID string
	err := h.pg.Pool.QueryRow(c.Context(), `
		INSERT INTO site_allowed_domains (site_id, domain, is_active)
		VALUES ($1, $2, true)
		ON CONFLICT (site_id, domain) DO UPDATE SET is_active = true
		RETURNING id::text
	`, siteID, domain).Scan(&newID)

	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": fmt.Sprintf("erro ao salvar dominio: %v", err)})
	}

	// Invalida cache para ativação em tempo real
	h.InvalidateSiteCache(siteID)

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"status":  "created",
		"id":      newID,
		"site_id": siteID,
		"domain":  domain,
	})
}

// HandleDeleteDomain remove ou desativa um domínio
func (h *Handler) HandleDeleteDomain(c *fiber.Ctx) error {
	id := c.Params("id")
	if id == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "id obrigatorio"})
	}

	if h.pg == nil || h.pg.Pool == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "banco de dados indisponivel"})
	}

	var siteID string
	err := h.pg.Pool.QueryRow(c.Context(), "DELETE FROM site_allowed_domains WHERE id = $1 RETURNING site_id::text", id).Scan(&siteID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "dominio nao encontrado"})
	}

	// Invalida cache
	h.InvalidateSiteCache(siteID)

	return c.Status(fiber.StatusOK).JSON(fiber.Map{"status": "deleted"})
}

// HandleApproveDomain aprova um domínio bloqueado direto dos alertas e remove o alerta correspondente
func (h *Handler) HandleApproveDomain(c *fiber.Ctx) error {
	var req struct {
		SiteID string `json:"site_id"`
		Domain string `json:"domain"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "dados invalidos"})
	}

	siteID := strings.TrimSpace(req.SiteID)
	domain := cleanHost(req.Domain)
	if siteID == "" || domain == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "site_id e domain sao obrigatorios"})
	}

	if h.pg == nil || h.pg.Pool == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "banco de dados indisponivel"})
	}

	// 1. Insere ou ativa no site_allowed_domains
	var newID string
	err := h.pg.Pool.QueryRow(c.Context(), `
		INSERT INTO site_allowed_domains (site_id, domain, is_active)
		VALUES ($1, $2, true)
		ON CONFLICT (site_id, domain) DO UPDATE SET is_active = true
		RETURNING id::text
	`, siteID, domain).Scan(&newID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": fmt.Sprintf("erro ao aprovar dominio: %v", err)})
	}

	// 2. Remove o alerta daquele domínio não autorizado para limpar o card
	_, _ = h.pg.Pool.Exec(c.Context(), "DELETE FROM site_domain_alerts WHERE site_id = $1 AND unauthorized_domain = $2", siteID, domain)

	// 3. Invalida o cache
	h.InvalidateSiteCache(siteID)

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"status":  "approved",
		"id":      newID,
		"site_id": siteID,
		"domain":  domain,
	})
}
