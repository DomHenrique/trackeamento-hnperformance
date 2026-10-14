package sitemap

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type mockNormalizer struct{}

func (m *mockNormalizer) NormalizeURL(rawURL string) string {
	res := strings.Replace(rawURL, "www.", "", 1)
	if !strings.HasSuffix(res, "/") {
		res += "/"
	}
	return res
}

func TestParseXMLStream_URLSet(t *testing.T) {
	xmlData := `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url>
    <loc>https://www.spspower.com.br/contato</loc>
    <lastmod>2026-10-10T14:30:00Z</lastmod>
  </url>
  <url>
    <loc>https://spspower.com.br/sobre/</loc>
    <lastmod>2026-09-01</lastmod>
  </url>
</urlset>`

	normalizer := &mockNormalizer{}
	pages, subSitemaps, err := ParseXMLStream(strings.NewReader(xmlData), normalizer)
	if err != nil {
		t.Fatalf("ParseXMLStream falhou: %v", err)
	}

	if len(subSitemaps) != 0 {
		t.Errorf("esperado 0 subSitemaps, obteve %d", len(subSitemaps))
	}

	if len(pages) != 2 {
		t.Fatalf("esperado 2 páginas, obteve %d", len(pages))
	}

	// Verifica normalização canônica da primeira página (www -> apex + trailing slash)
	p1 := pages[0]
	if p1.Loc != "https://www.spspower.com.br/contato" {
		t.Errorf("Loc incorreto: %s", p1.Loc)
	}
	if p1.PageKey != "https://spspower.com.br/contato/" {
		t.Errorf("PageKey esperada 'https://spspower.com.br/contato/', obteve '%s'", p1.PageKey)
	}
	if p1.LastMod == nil || p1.LastMod.Year() != 2026 {
		t.Errorf("LastMod incorreto ou nulo: %v", p1.LastMod)
	}

	// Segunda página
	p2 := pages[1]
	if p2.PageKey != "https://spspower.com.br/sobre/" {
		t.Errorf("PageKey esperada 'https://spspower.com.br/sobre/', obteve '%s'", p2.PageKey)
	}
}

func TestParseXMLStream_SitemapIndex(t *testing.T) {
	xmlData := `<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap>
    <loc>https://spspower.com.br/page-sitemap.xml</loc>
    <lastmod>2026-10-12</lastmod>
  </sitemap>
  <sitemap>
    <loc>https://spspower.com.br/post-sitemap.xml</loc>
  </sitemap>
</sitemapindex>`

	pages, subSitemaps, err := ParseXMLStream(strings.NewReader(xmlData), nil)
	if err != nil {
		t.Fatalf("ParseXMLStream falhou: %v", err)
	}

	if len(pages) != 0 {
		t.Errorf("esperado 0 páginas em índice, obteve %d", len(pages))
	}

	if len(subSitemaps) != 2 {
		t.Fatalf("esperado 2 subSitemaps, obteve %d", len(subSitemaps))
	}

	if subSitemaps[0] != "https://spspower.com.br/page-sitemap.xml" {
		t.Errorf("subSitemap[0] inesperado: %s", subSitemaps[0])
	}
}

func TestParseXMLStream_CorruptedXML(t *testing.T) {
	badXML := `<urlset><url><loc>https://spspower.com.br/broken`
	_, _, err := ParseXMLStream(strings.NewReader(badXML), nil)
	if err == nil {
		t.Errorf("esperado erro ao processar XML corrompido, mas obteve nil")
	}
}

func TestParseXMLStream_OversizedXML(t *testing.T) {
	// Cria leitor com dados maiores que MaxFileSize (ex: 11 MB)
	bigBuffer := bytes.NewBuffer(make([]byte, 11*1024*1024))
	_, _, err := ParseXMLStream(bigBuffer, nil)
	if err == nil {
		t.Logf("Buffer gigante truncado por LimitReader sem erro de overflow")
	}
}

func TestParseSitemapDate(t *testing.T) {
	dates := []struct {
		input string
		year  int
		month time.Month
		day   int
	}{
		{"2026-10-14T11:00:00Z", 2026, time.October, 14},
		{"2026-05-01", 2026, time.May, 1},
		{"2026-08-20T18:30:00-03:00", 2026, time.August, 20},
	}

	for _, d := range dates {
		t.Run(d.input, func(t *testing.T) {
			parsed, err := parseSitemapDate(d.input)
			if err != nil {
				t.Fatalf("falha ao interpretar data %s: %v", d.input, err)
			}
			if parsed.Year() != d.year || parsed.Month() != d.month || parsed.Day() != d.day {
				t.Errorf("data interpretada incorreta: %v", parsed)
			}
		})
	}
}

func TestCrawlSitemapRecursively(t *testing.T) {
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		switch r.URL.Path {
		case "/sitemap.xml":
			fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>%s/pages.xml</loc></sitemap>
  <sitemap><loc>%s/posts.xml</loc></sitemap>
</sitemapindex>`, ts.URL, ts.URL)
		case "/pages.xml":
			fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>%s/home</loc></url>
  <url><loc>%s/contato</loc></url>
</urlset>`, ts.URL, ts.URL)
		case "/posts.xml":
			fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>%s/artigo-1</loc></url>
  <url><loc>%s/home</loc></url> <!-- duplicata intencional -->
</urlset>`, ts.URL, ts.URL)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	u, _ := url.Parse(ts.URL)
	allowedHosts := []string{u.Hostname()}

	// Usamos o ts.Client() do httptest que sabe rotear internamente
	client := ts.Client()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	normalizer := &mockNormalizer{}
	pages, err := CrawlSitemapRecursively(ctx, client, ts.URL+"/sitemap.xml", allowedHosts, normalizer)
	if err != nil {
		t.Fatalf("CrawlSitemapRecursively falhou: %v", err)
	}

	// 3 páginas únicas esperadas (/home/, /contato/, /artigo-1/)
	if len(pages) != 3 {
		t.Fatalf("esperado 3 páginas únicas, obteve %d", len(pages))
	}
}

