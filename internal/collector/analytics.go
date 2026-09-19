package collector

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"tracking-engine/internal/storage"
)

// HandleAnalyticsOverview retorna os KPIs gerais, série temporal e fontes de tráfego
func (h *Handler) HandleAnalyticsOverview(c *fiber.Ctx) error {
	if h.ch == nil || h.ch.Conn == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "ClickHouse analítico indisponível",
		})
	}

	siteID := strings.TrimSpace(c.Query("site_id"))
	rangeStr := strings.TrimSpace(c.Query("range", "7d"))

	stats, err := h.ch.GetAnalyticsOverview(c.Context(), siteID, rangeStr)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": fmt.Sprintf("Erro ao consultar métricas: %v", err),
		})
	}

	return c.Status(fiber.StatusOK).JSON(stats)
}

// HandleAnalyticsPages lista as páginas monitoradas e suas taxas de visualização e conversão
func (h *Handler) HandleAnalyticsPages(c *fiber.Ctx) error {
	if h.ch == nil || h.ch.Conn == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "ClickHouse analítico indisponível",
		})
	}

	siteID := strings.TrimSpace(c.Query("site_id"))
	rangeStr := strings.TrimSpace(c.Query("range", "7d"))

	pages, err := h.ch.GetMonitoredPages(c.Context(), siteID, rangeStr)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": fmt.Sprintf("Erro ao consultar páginas monitoradas: %v", err),
		})
	}

	return c.Status(fiber.StatusOK).JSON(pages)
}

// HandleAnalyticsLeads retorna a lista cronológica de conversões com auditoria de parâmetros
func (h *Handler) HandleAnalyticsLeads(c *fiber.Ctx) error {
	if h.ch == nil || h.ch.Conn == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "ClickHouse analítico indisponível",
		})
	}

	siteID := strings.TrimSpace(c.Query("site_id"))
	rangeStr := strings.TrimSpace(c.Query("range", "7d"))
	limit := c.QueryInt("limit", 100)

	leads, err := h.ch.GetLeadsReport(c.Context(), siteID, rangeStr, limit)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": fmt.Sprintf("Erro ao consultar relatório de leads: %v", err),
		})
	}

	return c.Status(fiber.StatusOK).JSON(leads)
}

// HandleExportLeadsCSV gera e envia o streaming de download do arquivo CSV com auditoria
func (h *Handler) HandleExportLeadsCSV(c *fiber.Ctx) error {
	if h.ch == nil || h.ch.Conn == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "ClickHouse analítico indisponível",
		})
	}

	siteID := strings.TrimSpace(c.Query("site_id"))
	rangeStr := strings.TrimSpace(c.Query("range", "30d"))

	leads, err := h.ch.GetLeadsReport(c.Context(), siteID, rangeStr, 1000)
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

// Ensure storage import is kept clean
var _ = storage.ParseDateRange
