package collector

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"tracking-engine/internal/storage"
)

// validateOptionalSiteUUID valida se site_id, quando informado, é um UUID v4/rfc4122 válido.
// Se vazio, retorna nil (representa "todos os domínios").
func validateOptionalSiteUUID(siteID string) error {
	if siteID == "" {
		return nil
	}
	if _, err := uuid.Parse(siteID); err != nil {
		return fmt.Errorf("site_id inválido: deve ser um UUID válido ou vazio")
	}
	return nil
}

// HandleAnalyticsOverview retorna os KPIs gerais, série temporal e fontes de tráfego
func (h *Handler) HandleAnalyticsOverview(c *fiber.Ctx) error {
	siteID := strings.TrimSpace(c.Query("site_id"))
	if err := validateOptionalSiteUUID(siteID); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	if h.ch == nil || h.ch.Conn == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "ClickHouse analítico indisponível",
		})
	}

	domain := strings.TrimSpace(c.Query("domain"))
	rangeStr := strings.TrimSpace(c.Query("range", "7d"))

	stats, err := h.ch.GetAnalyticsOverview(c.Context(), siteID, domain, rangeStr)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": fmt.Sprintf("Erro ao consultar métricas: %v", err),
		})
	}

	return c.Status(fiber.StatusOK).JSON(stats)
}

// HandleAnalyticsPages lista o relatório de páginas monitoradas com séries temporais e canonicalização
func (h *Handler) HandleAnalyticsPages(c *fiber.Ctx) error {
	siteID := strings.TrimSpace(c.Query("site_id"))
	if err := validateOptionalSiteUUID(siteID); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	if h.ch == nil || h.ch.Conn == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "ClickHouse analítico indisponível",
		})
	}

	domain := strings.TrimSpace(c.Query("domain"))
	rangeStr := strings.TrimSpace(c.Query("range", "7d"))

	report, err := h.ch.GetMonitoredPagesReport(c.Context(), siteID, domain, rangeStr)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": fmt.Sprintf("Erro ao consultar páginas monitoradas: %v", err),
		})
	}

	// Enriquece o relatório com o inventário do catálogo e sitemap do PostgreSQL
	if h.pg != nil && h.pg.Pool != nil {
		targetSiteID := siteID
		if targetSiteID == "" && domain != "" {
			_ = h.pg.Pool.QueryRow(c.Context(), "SELECT id::text FROM sites WHERE domain = $1 LIMIT 1", domain).Scan(&targetSiteID)
		}

		if targetSiteID != "" {
			rows, err := h.pg.Pool.Query(c.Context(), `
				SELECT page_key, in_sitemap, source, COALESCE(title, '')
				FROM site_page_catalog
				WHERE site_id = $1
			`, targetSiteID)

			if err == nil {
				type catInfo struct {
					inSitemap bool
					source    string
					title     string
				}
				catalogMap := make(map[string]catInfo)
				sitemapTotal := 0

				for rows.Next() {
					var pk, src, title string
					var inSite bool
					if err := rows.Scan(&pk, &inSite, &src, &title); err == nil {
						catalogMap[pk] = catInfo{inSitemap: inSite, source: src, title: title}
						if inSite {
							sitemapTotal++
						}
					}
				}
				rows.Close()

				sitemapCoveredCount := 0
				for i := range report.Pages {
					p := &report.Pages[i]
					if info, exists := catalogMap[p.PageURL]; exists {
						p.InSitemap = info.inSitemap
						p.Source = info.source
						if p.PageTitle == "" && info.title != "" {
							p.PageTitle = info.title
						}
					} else {
						p.InSitemap = false
						p.Source = "traffic"
					}

					if p.InSitemap && p.Pageviews > 0 {
						sitemapCoveredCount++
					}
				}

				report.Summary.SitemapTotalPages = sitemapTotal
				report.Summary.SitemapCoveredPages = sitemapCoveredCount
				if sitemapTotal > 0 {
					rate := (float64(sitemapCoveredCount) / float64(sitemapTotal)) * 100
					report.Summary.SitemapCoverageRate = math.Round(rate*100) / 100
				}
				if sitemapTotal >= sitemapCoveredCount {
					report.Summary.ZeroTrafficPagesCount = sitemapTotal - sitemapCoveredCount
				}
			}
		}
	}

	return c.Status(fiber.StatusOK).JSON(report)
}

// HandleAnalyticsLeads retorna a lista cronológica de conversões com auditoria de parâmetros
func (h *Handler) HandleAnalyticsLeads(c *fiber.Ctx) error {
	siteID := strings.TrimSpace(c.Query("site_id"))
	if err := validateOptionalSiteUUID(siteID); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	if h.ch == nil || h.ch.Conn == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "ClickHouse analítico indisponível",
		})
	}

	domain := strings.TrimSpace(c.Query("domain"))
	rangeStr := strings.TrimSpace(c.Query("range", "7d"))
	limit := c.QueryInt("limit", 100)

	leads, err := h.ch.GetLeadsReport(c.Context(), siteID, domain, rangeStr, limit)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": fmt.Sprintf("Erro ao consultar relatório de leads: %v", err),
		})
	}

	return c.Status(fiber.StatusOK).JSON(leads)
}

// HandleExportLeadsCSV gera e envia o streaming de download do arquivo CSV com auditoria
func (h *Handler) HandleExportLeadsCSV(c *fiber.Ctx) error {
	siteID := strings.TrimSpace(c.Query("site_id"))
	if err := validateOptionalSiteUUID(siteID); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	if h.ch == nil || h.ch.Conn == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "ClickHouse analítico indisponível",
		})
	}

	domain := strings.TrimSpace(c.Query("domain"))
	rangeStr := strings.TrimSpace(c.Query("range", "30d"))

	leads, err := h.ch.GetLeadsReport(c.Context(), siteID, domain, rangeStr, 1000)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": fmt.Sprintf("Erro ao gerar relatório para exportação: %v", err),
		})
	}

	var buf bytes.Buffer
	// Escreve UTF-8 BOM para garantir compatibilidade com Excel no Windows/Mac
	buf.Write([]byte{0xEF, 0xBB, 0xBF})

	writer := csv.NewWriter(&buf)
	writer.Comma = ';' // Padrão Excel em português

	// Cabeçalhos das Colunas
	header := []string{
		"Data e Hora",
		"Tipo de Conversao",
		"Pagina de Conversao",
		"Landing Page",
		"Origem (utm_source)",
		"Midia (utm_medium)",
		"Campanha (utm_campaign)",
		"Google Click ID (GCLID)",
		"Meta Click ID (FBCLID)",
		"Dispositivo",
		"IP do Visitante",
		"Status Auditoria",
		"Alertas de Midia Paga",
	}
	_ = writer.Write(header)

	for _, l := range leads {
		statusLabel := "OK (Rastreamento Completo)"
		if l.IntegrityLevel == "warning_high" {
			statusLabel = "CRITICO: Sem UTM e Sem Click ID"
		} else if l.IntegrityLevel == "warning_medium" {
			statusLabel = "AVISO: Parametros Incompletos"
		}

		alertsStr := strings.Join(l.MissingAlerts, ", ")
		if alertsStr == "" {
			alertsStr = "Nenhum"
		}

		row := []string{
			l.EventTime.Format("02/01/2006 15:04:05"),
			l.EventName,
			l.PageURL,
			l.LandingPage,
			l.UTMSource,
			l.UTMMedium,
			l.UTMCampaign,
			l.GCLID,
			l.FBCLID,
			l.DeviceType,
			l.IPAddress,
			statusLabel,
			alertsStr,
		}
		_ = writer.Write(row)
	}
	writer.Flush()

	filename := fmt.Sprintf("leads_hn_performance_%s.csv", time.Now().Format("20060102_150405"))
	c.Set("Content-Type", "text/csv; charset=utf-8")
	c.Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	c.Set("Cache-Control", "no-store")

	return c.Send(buf.Bytes())
}

// HandleAnalyticsFunnel retorna as 4 etapas de conversão e taxas de abandono
func (h *Handler) HandleAnalyticsFunnel(c *fiber.Ctx) error {
	siteID := strings.TrimSpace(c.Query("site_id"))
	if err := validateOptionalSiteUUID(siteID); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	if h.ch == nil || h.ch.Conn == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "ClickHouse analítico indisponível",
		})
	}

	domain := strings.TrimSpace(c.Query("domain"))
	rangeStr := strings.TrimSpace(c.Query("range", "7d"))

	funnel, err := h.ch.GetFunnelAnalysis(c.Context(), siteID, domain, rangeStr)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": fmt.Sprintf("Erro ao consultar análise de funil: %v", err),
		})
	}

	return c.Status(fiber.StatusOK).JSON(funnel)
}

// HandleAnalyticsAttributionPaths retorna as top sequências de canais e comparativo First vs Last Touch
func (h *Handler) HandleAnalyticsAttributionPaths(c *fiber.Ctx) error {
	siteID := strings.TrimSpace(c.Query("site_id"))
	if err := validateOptionalSiteUUID(siteID); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	if h.ch == nil || h.ch.Conn == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "ClickHouse analítico indisponível",
		})
	}

	domain := strings.TrimSpace(c.Query("domain"))
	rangeStr := strings.TrimSpace(c.Query("range", "7d"))
	limit := c.QueryInt("limit", 15)

	paths, err := h.ch.GetAttributionPaths(c.Context(), siteID, domain, rangeStr, limit)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": fmt.Sprintf("Erro ao consultar caminhos de atribuição: %v", err),
		})
	}

	return c.Status(fiber.StatusOK).JSON(paths)
}

// HandleAnalyticsVisitorJourney retorna todos os eventos ordenados cronologicamente de um visitante
func (h *Handler) HandleAnalyticsVisitorJourney(c *fiber.Ctx) error {
	visitorID := strings.TrimSpace(c.Query("visitor_id"))
	siteID := strings.TrimSpace(c.Query("site_id"))
	if err := validateOptionalSiteUUID(siteID); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	if h.ch == nil || h.ch.Conn == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "ClickHouse analítico indisponível",
		})
	}

	if visitorID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "O parâmetro 'visitor_id' é obrigatório",
		})
	}

	journey, err := h.ch.GetVisitorJourney(c.Context(), visitorID, siteID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": fmt.Sprintf("Erro ao consultar jornada do visitante: %v", err),
		})
	}

	return c.Status(fiber.StatusOK).JSON(journey)
}

// Ensure storage import is kept clean
var _ = storage.ParseDateRange

