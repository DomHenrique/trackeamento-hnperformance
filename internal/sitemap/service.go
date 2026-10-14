package sitemap

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"tracking-engine/internal/storage"
)

// SyncResult sumariza o resultado da sincronização de sitemap
type SyncResult struct {
	SiteID     string    `json:"site_id"`
	SitemapURL string    `json:"sitemap_url"`
	TotalURLs  int       `json:"total_urls"`
	SyncedAt   time.Time `json:"synced_at"`
}

// CatalogEntry representa uma página catalogada no inventário do site
type CatalogEntry struct {
	ID                  string     `json:"id"`
	SiteID              string     `json:"site_id"`
	PageKey             string     `json:"page_key"`
	CanonicalURL        string     `json:"canonical_url"`
	Title               string     `json:"title"`
	InSitemap           bool       `json:"in_sitemap"`
	Source              string     `json:"source"`
	Status              string     `json:"status"`
	LastSeenInSitemapAt *time.Time `json:"last_seen_in_sitemap_at"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// Service orquestra a sincronização de sitemaps e o catálogo de páginas
type Service struct {
	pg *storage.PostgresDB
}

// NewService instancia o serviço de sitemaps
func NewService(pg *storage.PostgresDB) *Service {
	return &Service{pg: pg}
}

// SyncSiteSitemap executa o download seguro, parsing recursivo e upsert em lote do sitemap do site
func (s *Service) SyncSiteSitemap(ctx context.Context, siteID string, overrideSitemapURL string, normalizer URLNormalizer, extraAllowedHosts ...string) (*SyncResult, error) {
	if s.pg == nil || s.pg.Pool == nil {
		return nil, fmt.Errorf("banco de dados postgres indisponivel")
	}

	siteID = strings.TrimSpace(siteID)
	if siteID == "" {
		return nil, fmt.Errorf("site_id e obrigatorio")
	}

	// 1. Carrega dados do site
	var domain, dbSitemapURL string
	var rawCanonical []byte
	row := s.pg.Pool.QueryRow(ctx, `
		SELECT domain, COALESCE(sitemap_url, ''), COALESCE(canonical_settings, '{}'::jsonb)
		FROM sites
		WHERE id = $1
	`, siteID)
	if err := row.Scan(&domain, &dbSitemapURL, &rawCanonical); err != nil {
		return nil, fmt.Errorf("site '%s' nao encontrado: %w", siteID, err)
	}

	// 2. Determina URL do sitemap
	targetSitemapURL := strings.TrimSpace(overrideSitemapURL)
	if targetSitemapURL == "" {
		targetSitemapURL = strings.TrimSpace(dbSitemapURL)
	}
	if targetSitemapURL == "" {
		cleanDomain := strings.TrimSpace(domain)
		if cleanDomain == "" {
			return nil, fmt.Errorf("site sem dominio principal configurado")
		}
		targetSitemapURL = fmt.Sprintf("https://%s/sitemap.xml", cleanDomain)
	}

	// 3. Extrai hosts autorizados de canonical_settings
	type rawCan struct {
		CanonicalHost string   `json:"canonical_host"`
		HostAliases   []string `json:"host_aliases"`
	}
	var canConfig rawCan
	if len(rawCanonical) > 0 && string(rawCanonical) != "{}" {
		_ = json.Unmarshal(rawCanonical, &canConfig)
	}

	// 4. Carrega todos os domínios autorizados do site (canônico, aliases, permitidos)
	allowedHostsMap := make(map[string]bool)
	if canConfig.CanonicalHost != "" {
		allowedHostsMap[strings.ToLower(strings.TrimSpace(canConfig.CanonicalHost))] = true
	}
	if domain != "" {
		allowedHostsMap[strings.ToLower(strings.TrimSpace(domain))] = true
	}
	for _, alias := range canConfig.HostAliases {
		c := strings.ToLower(strings.TrimSpace(alias))
		if c != "" {
			allowedHostsMap[c] = true
		}
	}
	for _, h := range extraAllowedHosts {
		c := strings.ToLower(strings.TrimSpace(h))
		if c != "" {
			allowedHostsMap[c] = true
		}
	}

	// Busca domínios adicionais na tabela site_allowed_domains
	rows, err := s.pg.Pool.Query(ctx, `SELECT domain FROM site_allowed_domains WHERE site_id = $1 AND is_active = true`, siteID)
	if err == nil {
		for rows.Next() {
			var d string
			if err := rows.Scan(&d); err == nil {
				cleanD := strings.ToLower(strings.TrimSpace(d))
				if cleanD != "" {
					allowedHostsMap[cleanD] = true
				}
			}
		}
		rows.Close()
	}

	allowedHosts := make([]string, 0, len(allowedHostsMap))
	for h := range allowedHostsMap {
		allowedHosts = append(allowedHosts, h)
	}

	// 5. Marca status como 'syncing' no banco
	_, _ = s.pg.Pool.Exec(ctx, `
		UPDATE sites
		SET sitemap_status = 'syncing', sitemap_url = $1, last_sitemap_error = NULL, updated_at = now()
		WHERE id = $2
	`, targetSitemapURL, siteID)

	// 6. Instancia cliente seguro anti-SSRF e faz o crawl recursivo
	client := NewSafeHTTPClient(allowedHosts, 30*time.Second)
	pages, crawlErr := CrawlSitemapRecursively(ctx, client, targetSitemapURL, allowedHosts, normalizer)
	if crawlErr != nil {
		errMsg := crawlErr.Error()
		_, _ = s.pg.Pool.Exec(ctx, `
			UPDATE sites
			SET sitemap_status = 'error', last_sitemap_error = $1, updated_at = now()
			WHERE id = $2
		`, errMsg, siteID)
		return nil, fmt.Errorf("falha ao processar sitemap: %w", crawlErr)
	}

	// 7. Upsert em lote no PostgreSQL (site_page_catalog)
	now := time.Now()
	batchSize := 500
	totalInserted := 0

	for i := 0; i < len(pages); i += batchSize {
		end := i + batchSize
		if end > len(pages) {
			end = len(pages)
		}
		chunk := pages[i:end]

		// Constrói query multi-rows
		var b strings.Builder
		b.WriteString(`
			INSERT INTO site_page_catalog (
				site_id, page_key, canonical_url, in_sitemap, source, status, last_seen_in_sitemap_at, updated_at
			) VALUES `)

		vals := make([]interface{}, 0, len(chunk)*5)
		idx := 1
		for j, p := range chunk {
			if j > 0 {
				b.WriteString(", ")
			}
			b.WriteString(fmt.Sprintf("($%d, $%d, $%d, true, 'sitemap', 'monitored', $%d, $%d)", idx, idx+1, idx+2, idx+3, idx+4))
			vals = append(vals, siteID, p.PageKey, p.Loc, now, now)
			idx += 5
		}

		b.WriteString(`
			ON CONFLICT (site_id, page_key) DO UPDATE SET
				in_sitemap = true,
				canonical_url = EXCLUDED.canonical_url,
				last_seen_in_sitemap_at = EXCLUDED.last_seen_in_sitemap_at,
				updated_at = EXCLUDED.updated_at
		`)

		_, err := s.pg.Pool.Exec(ctx, b.String(), vals...)
		if err != nil {
			log.Printf("[SITEMAP] Erro ao persistir lote de páginas no catalogo: %v", err)
			_, _ = s.pg.Pool.Exec(ctx, `
				UPDATE sites
				SET sitemap_status = 'error', last_sitemap_error = $1, updated_at = now()
				WHERE id = $2
			`, fmt.Sprintf("erro ao persistir no catalogo: %v", err), siteID)
			return nil, fmt.Errorf("erro ao salvar paginas no catalogo: %w", err)
		}
		totalInserted += len(chunk)
	}

	// 8. Atualiza status de sucesso em sites
	_, err = s.pg.Pool.Exec(ctx, `
		UPDATE sites
		SET sitemap_status = 'synced',
		    sitemap_url = $1,
		    last_sitemap_sync_at = $2,
		    sitemap_total_urls = $3,
		    last_sitemap_error = NULL,
		    updated_at = now()
		WHERE id = $4
	`, targetSitemapURL, now, totalInserted, siteID)
	if err != nil {
		log.Printf("[SITEMAP] Erro ao atualizar status final do site: %v", err)
	}

	return &SyncResult{
		SiteID:     siteID,
		SitemapURL: targetSitemapURL,
		TotalURLs:  totalInserted,
		SyncedAt:   now,
	}, nil
}

// GetSiteCatalog lista as páginas do catálogo de um site com suporte a paginação e filtro
func (s *Service) GetSiteCatalog(ctx context.Context, siteID string, limit, offset int, inSitemapOnly bool) ([]CatalogEntry, int, error) {
	if s.pg == nil || s.pg.Pool == nil {
		return nil, 0, fmt.Errorf("banco de dados postgres indisponivel")
	}

	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	whereClause := "WHERE site_id = $1"
	args := []interface{}{siteID}

	if inSitemapOnly {
		whereClause += " AND in_sitemap = true"
	}

	// Contagem total
	var total int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM site_page_catalog %s", whereClause)
	if err := s.pg.Pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("falha ao contar paginas do catalogo: %w", err)
	}

	// Lista de páginas ordenada por in_sitemap DESC, page_key ASC
	selectQuery := fmt.Sprintf(`
		SELECT id::text, site_id::text, page_key, canonical_url, COALESCE(title, ''),
		       in_sitemap, source, status, last_seen_in_sitemap_at, created_at, updated_at
		FROM site_page_catalog
		%s
		ORDER BY in_sitemap DESC, page_key ASC
		LIMIT $%d OFFSET $%d
	`, whereClause, len(args)+1, len(args)+2)

	queryArgs := append(args, limit, offset)
	rows, err := s.pg.Pool.Query(ctx, selectQuery, queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("falha ao consultar catalogo: %w", err)
	}
	defer rows.Close()

	entries := make([]CatalogEntry, 0)
	for rows.Next() {
		var e CatalogEntry
		if err := rows.Scan(
			&e.ID, &e.SiteID, &e.PageKey, &e.CanonicalURL, &e.Title,
			&e.InSitemap, &e.Source, &e.Status, &e.LastSeenInSitemapAt,
			&e.CreatedAt, &e.UpdatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("falha ao ler linha do catalogo: %w", err)
		}
		entries = append(entries, e)
	}

	return entries, total, nil
}

// AutoRegisterTrafficPage registra ou atualiza uma página descoberta em tempo real pelo coletor
func (s *Service) AutoRegisterTrafficPage(ctx context.Context, siteID, pageKey, canonicalURL, title string) error {
	if s.pg == nil || s.pg.Pool == nil {
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

	_, err := s.pg.Pool.Exec(ctx, `
		INSERT INTO site_page_catalog (
			site_id, page_key, canonical_url, title, in_sitemap, source, status, created_at, updated_at
		) VALUES ($1, $2, $3, $4, false, 'traffic', 'monitored', now(), now())
		ON CONFLICT (site_id, page_key) DO UPDATE SET
			title = CASE WHEN EXCLUDED.title != '' THEN EXCLUDED.title ELSE site_page_catalog.title END,
			updated_at = now()
	`, siteID, cleanKey, cleanCanonical, cleanTitle)

	return err
}
