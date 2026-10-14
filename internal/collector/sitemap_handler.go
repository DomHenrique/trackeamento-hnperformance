package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"tracking-engine/internal/sitemap"
)

// SitemapSyncRequest payload opcional para o endpoint de sincronização
type SitemapSyncRequest struct {
	SitemapURL string `json:"sitemap_url"`
}

// HandleSitemapSync aciona a sincronização manual do sitemap de um site
func (h *Handler) HandleSitemapSync(c *fiber.Ctx) error {
	siteID := strings.TrimSpace(c.Params("id"))
	if siteID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "id do site e obrigatorio"})
	}

	var req SitemapSyncRequest
	_ = c.BodyParser(&req)

	if h.pg == nil || h.pg.Pool == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "banco de dados indisponivel"})
	}

	svc := sitemap.NewService(h.pg)

	ctx, cancel := context.WithTimeout(c.Context(), 45*time.Second)
	defer cancel()

	var domain string
	var rawCanonical []byte
	row := h.pg.Pool.QueryRow(ctx, "SELECT domain, COALESCE(canonical_settings, '{}'::jsonb) FROM sites WHERE id = $1", siteID)
	if err := row.Scan(&domain, &rawCanonical); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "site nao encontrado"})
	}

	canSettings := DefaultCanonicalSettings(domain)
	if len(rawCanonical) > 0 && string(rawCanonical) != "{}" {
		_ = json.Unmarshal(rawCanonical, canSettings)
	}

	result, err := svc.SyncSiteSitemap(ctx, siteID, req.SitemapURL, canSettings)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error":   fmt.Sprintf("erro ao sincronizar sitemap: %v", err),
			"site_id": siteID,
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"message":     "sitemap sincronizado com sucesso",
		"site_id":     result.SiteID,
		"sitemap_url": result.SitemapURL,
		"total_urls":  result.TotalURLs,
		"synced_at":   result.SyncedAt,
	})
}

// HandleGetSiteCatalog retorna o inventário de páginas do catálogo do site
func (h *Handler) HandleGetSiteCatalog(c *fiber.Ctx) error {
	siteID := strings.TrimSpace(c.Params("id"))
	if siteID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "id do site e obrigatorio"})
	}

	if h.pg == nil || h.pg.Pool == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "banco de dados indisponivel"})
	}

	limit := 100
	if l := c.Query("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 1000 {
			limit = parsed
		}
	}

	offset := 0
	if o := c.Query("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	inSitemapOnly := false
	if s := c.Query("in_sitemap"); s == "true" || s == "1" {
		inSitemapOnly = true
	}

	svc := sitemap.NewService(h.pg)
	entries, total, err := svc.GetSiteCatalog(c.Context(), siteID, limit, offset, inSitemapOnly)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": fmt.Sprintf("falha ao consultar catalogo: %v", err),
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"site_id": siteID,
		"total":   total,
		"limit":   limit,
		"offset":  offset,
		"pages":   entries,
	})
}

// autoRegisterTrafficPage insere ou atualiza de forma assíncrona uma página descoberta por tráfego real
func (h *Handler) autoRegisterTrafficPage(ctx context.Context, siteID, pageKey, canonicalURL, title string) error {
	if h.pg == nil || h.pg.Pool == nil {
		return nil
	}
	cleanKey := strings.TrimSpace(pageKey)
	if cleanKey == "" || strings.TrimSpace(siteID) == "" {
		return nil
	}
	cleanCanonical := strings.TrimSpace(canonicalURL)
	if cleanCanonical == "" {
		cleanCanonical = cleanKey
	}
	cleanTitle := strings.TrimSpace(title)

	_, err := h.pg.Pool.Exec(ctx, `
		INSERT INTO site_page_catalog (
			site_id, page_key, canonical_url, title, in_sitemap, source, status, created_at, updated_at
		) VALUES ($1, $2, $3, $4, false, 'traffic', 'monitored', now(), now())
		ON CONFLICT (site_id, page_key) DO UPDATE SET
			title = CASE WHEN EXCLUDED.title != '' THEN EXCLUDED.title ELSE site_page_catalog.title END,
			updated_at = now()
	`, siteID, cleanKey, cleanCanonical, cleanTitle)

	return err
}
