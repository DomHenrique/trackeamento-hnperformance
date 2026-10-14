package sitemap

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// URLNormalizer desacopla o parser do pacote collector
type URLNormalizer interface {
	NormalizeURL(rawURL string) string
}

const (
	// MaxFileSize: 10 MB por sitemap para evitar exaustão de memória
	MaxFileSize = 10 * 1024 * 1024
	// MaxSubSitemaps: Limite máximo de sub-sitemaps processados por índice
	MaxSubSitemaps = 50
	// MaxTotalURLs: Limite máximo de URLs catalogadas por sincronização
	MaxTotalURLs = 50000
	// MaxRecursionDepth: Limite de profundidade para sub-sitemaps aninhados
	MaxRecursionDepth = 3
)

// ParsedPage representa uma página descoberta no sitemap
type ParsedPage struct {
	Loc       string
	PageKey   string
	LastMod   *time.Time
}

// XMLLocNode auxilia na leitura genérica de <loc> e <lastmod>
type XMLItem struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod"`
}

// ParseXMLStream faz o parsing em streaming de um leitor XML (urlset ou sitemapindex)
// Retorna (páginas_encontradas, sub_sitemaps_encontrados, error)
func ParseXMLStream(r io.Reader, normalizer URLNormalizer) ([]ParsedPage, []string, error) {
	limitedReader := io.LimitReader(r, MaxFileSize)
	decoder := xml.NewDecoder(limitedReader)

	var pages []ParsedPage
	var subSitemaps []string

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("erro no processamento do XML: %w", err)
		}

		se, ok := token.(xml.StartElement)
		if !ok {
			continue
		}

		tagName := strings.ToLower(se.Name.Local)

		switch tagName {
		case "url":
			var item XMLItem
			if err := decoder.DecodeElement(&item, &se); err == nil {
				loc := strings.TrimSpace(item.Loc)
				if loc != "" {
					var lastMod *time.Time
					if item.LastMod != "" {
						if t, err := parseSitemapDate(item.LastMod); err == nil {
							lastMod = &t
						}
					}

					pageKey := loc
					if normalizer != nil {
						pageKey = normalizer.NormalizeURL(loc)
					}

					pages = append(pages, ParsedPage{
						Loc:     loc,
						PageKey: pageKey,
						LastMod: lastMod,
					})
				}
			}

		case "sitemap":
			var item XMLItem
			if err := decoder.DecodeElement(&item, &se); err == nil {
				loc := strings.TrimSpace(item.Loc)
				if loc != "" {
					subSitemaps = append(subSitemaps, loc)
				}
			}
		}
	}

	return pages, subSitemaps, nil
}

// CrawlSitemapRecursively orquestra a busca e parsing em cascata a partir do sitemap raiz
func CrawlSitemapRecursively(ctx context.Context, client *http.Client, startURL string, allowedHosts []string, normalizer URLNormalizer) ([]ParsedPage, error) {
	visitedSitemaps := make(map[string]bool)
	uniquePages := make(map[string]ParsedPage)

	queue := []struct {
		url   string
		depth int
	}{
		{url: startURL, depth: 1},
	}

	for len(queue) > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		curr := queue[0]
		queue = queue[1:]

		if visitedSitemaps[curr.url] {
			continue
		}
		visitedSitemaps[curr.url] = true

		if len(visitedSitemaps) > MaxSubSitemaps {
			break
		}

		// Requisição segura anti-SSRF
		resp, err := SafeFetch(ctx, client, curr.url, allowedHosts)
		if err != nil {
			// Se o sitemap raiz falhar, retorna erro; se for um sub-sitemap secundário, loga e continua
			if curr.depth == 1 && len(visitedSitemaps) == 1 {
				return nil, fmt.Errorf("falha ao obter sitemap raiz (%s): %w", curr.url, err)
			}
			continue
		}

		pages, subSitemaps, err := ParseXMLStream(resp.Body, normalizer)
		_ = resp.Body.Close()
		if err != nil {
			if curr.depth == 1 && len(visitedSitemaps) == 1 {
				return nil, fmt.Errorf("falha ao interpretar sitemap raiz (%s): %w", curr.url, err)
			}
			continue
		}

		// Coleta as páginas com deduplicação por PageKey
		for _, p := range pages {
			if p.PageKey == "" {
				continue
			}
			if _, exists := uniquePages[p.PageKey]; !exists {
				uniquePages[p.PageKey] = p
				if len(uniquePages) >= MaxTotalURLs {
					break
				}
			}
		}

		if len(uniquePages) >= MaxTotalURLs {
			break
		}

		// Enfileira sub-sitemaps respeitando o limite de profundidade
		if curr.depth < MaxRecursionDepth {
			for _, sub := range subSitemaps {
				if !visitedSitemaps[sub] && len(visitedSitemaps)+len(queue) < MaxSubSitemaps {
					queue = append(queue, struct {
						url   string
						depth int
					}{url: sub, depth: curr.depth + 1})
				}
			}
		}
	}

	result := make([]ParsedPage, 0, len(uniquePages))
	for _, p := range uniquePages {
		result = append(result, p)
	}

	return result, nil
}

// parseSitemapDate aceita formatos ISO8601/RFC3339 com ou sem hora/fuso
func parseSitemapDate(raw string) (time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	formats := []string{
		time.RFC3339,
		"2006-01-02T15:04:05-07:00",
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05",
		"2006-01-02",
	}

	for _, f := range formats {
		if t, err := time.Parse(f, trimmed); err == nil {
			return t, nil
		}
	}

	return time.Time{}, fmt.Errorf("formato de data desconhecido: %s", raw)
}
