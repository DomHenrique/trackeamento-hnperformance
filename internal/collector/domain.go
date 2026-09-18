package collector

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

type SiteMetadata struct {
	ID             string
	AllowedDomains []string
}

// ExtractOriginDomain extrai o domínio limpo da requisição com base em Origin -> Referer -> URL
func ExtractOriginDomain(c *fiber.Ctx, req *EventRequest) string {
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

	// 3. Tenta campos do payload (URL ou PageURL)
	pageURL := req.URL
	if pageURL == "" {
		pageURL = req.PageURL
	}
	if pageURL != "" {
		if d := cleanHost(pageURL); d != "" {
			return d
		}
	}

	// 4. Referrer do payload se houver
	if req.Referrer != "" {
		if d := cleanHost(req.Referrer); d != "" {
			return d
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

	siteKey := strings.TrimSpace(c.Query("site_key"))
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
