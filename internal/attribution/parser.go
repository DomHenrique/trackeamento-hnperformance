// Package attribution extrai e normaliza parâmetros de campanha e click IDs
// a partir da URL da página e do referrer.
//
// Decisões de privacidade (LGPD):
//   - PageURL e Referrer são reconstruídos só com o que é necessário; qualquer
//     outro parâmetro de query (email, token, cpf...) é descartado antes de
//     armazenar ou encaminhar para Meta, Google ou CRM.
//   - Fragmento (#...) e credenciais embutidas (user:pass@) nunca são mantidos.
package attribution

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxInputLen     = 8192 // URLs maiores que isso são ignoradas
	maxTextLen      = 256  // utm_*
	maxClickIDLen   = 512  // click IDs
	maxStoredURLLen = 2048 // PageURL, LandingPage, Referrer
)

// Click IDs diferenciam maiúsculas de minúsculas: nunca aplicar lowercase.
// Se alguma plataforma passar a usar outros caracteres, amplie o padrão aqui.
var clickIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Canais retornados em Params.Channel.
const (
	ChannelDirect        = "direct"
	ChannelPaidSearch    = "paid_search"
	ChannelPaidSocial    = "paid_social"
	ChannelPaidOther     = "paid_other"
	ChannelEmail         = "email"
	ChannelOrganicSearch = "organic_search"
	ChannelOrganicSocial = "organic_social"
	ChannelReferral      = "referral"
)

type Params struct {
	LandingPage string `json:"landing_page"`
	PageURL     string `json:"page_url"`
	Referrer    string `json:"referrer"`
	UTMSource   string `json:"utm_source"`
	UTMMedium   string `json:"utm_medium"`
	UTMCampaign string `json:"utm_campaign"`
	UTMContent  string `json:"utm_content"`
	UTMTerm     string `json:"utm_term"`
	GCLID       string `json:"gclid"`
	GBRAID      string `json:"gbraid"`
	WBRAID      string `json:"wbraid"`
	FBCLID      string `json:"fbclid"`
	TTCLID      string `json:"ttclid"`

	LIFATID       string `json:"li_fat_id,omitempty"`
	MSCLKID       string `json:"msclkid,omitempty"`
	DCLID         string `json:"dclid,omitempty"`
	GadSource     string `json:"gad_source,omitempty"`
	GadCampaignID string `json:"gad_campaignid,omitempty"`

	// Channel é a classificação do tráfego desta visita (ver constantes Channel*).
	Channel string `json:"channel,omitempty"`
	// ReferrerHost é o host do referrer, em minúsculas.
	ReferrerHost string `json:"referrer_host,omitempty"`
	// IgnoredReferrer é true quando o referrer é do próprio site ou de um
	// gateway de pagamento. Nesse caso o chamador NÃO deve sobrescrever a
	// origem da sessão/first-touch com esta visita.
	IgnoredReferrer bool `json:"ignored_referrer,omitempty"`
}

// HasClickID informa se a visita trouxe algum click ID de anúncio. Quando for
// true, o chamador deve guardar também o instante da captura: o gclid, por
// exemplo, só serve para conversão offline dentro de uma janela limitada.
func (p Params) HasClickID() bool {
	return p.GCLID != "" || p.GBRAID != "" || p.WBRAID != "" || p.FBCLID != "" ||
		p.TTCLID != "" || p.LIFATID != "" || p.MSCLKID != "" || p.DCLID != ""
}

// Options configura a análise por deploy/cliente.
type Options struct {
	// OwnHosts são os domínios do próprio site (ex.: "cliente.com.br"). Informe
	// o domínio raiz: subdomínios casam automaticamente.
	OwnHosts []string
	// IgnoredReferrerHosts são domínios extras cujo referrer não deve alterar
	// a origem (além dos gateways de pagamento já embutidos).
	IgnoredReferrerHosts []string
}

// ParseURL mantém a assinatura original. Equivale a ParseURLWithOptions com
// Options vazio (sem domínios próprios).
func ParseURL(rawURL, rawReferrer string) Params {
	return ParseURLWithOptions(rawURL, rawReferrer, Options{})
}

// ParseURLWithOptions analisa a URL da página e o referrer.
func ParseURLWithOptions(rawURL, rawReferrer string, opts Options) Params {
	var p Params
	p.applyReferrer(rawReferrer, opts)
	if u, ok := parseHTTPURL(rawURL); ok {
		p.applyPage(u)
	}
	p.Channel = classify(p)
	return p
}

func (p *Params) targets() map[string]*string {
	return map[string]*string{
		"utm_source":     &p.UTMSource,
		"utm_medium":     &p.UTMMedium,
		"utm_campaign":   &p.UTMCampaign,
		"utm_content":    &p.UTMContent,
		"utm_term":       &p.UTMTerm,
		"gclid":          &p.GCLID,
		"gbraid":         &p.GBRAID,
		"wbraid":         &p.WBRAID,
		"fbclid":         &p.FBCLID,
		"ttclid":         &p.TTCLID,
		"li_fat_id":      &p.LIFATID,
		"msclkid":        &p.MSCLKID,
		"dclid":          &p.DCLID,
		"gad_source":     &p.GadSource,
		"gad_campaignid": &p.GadCampaignID,
	}
}

func (p *Params) applyPage(u *url.URL) {
	base := u.Scheme + "://" + strings.ToLower(u.Host) + u.EscapedPath()
	p.LandingPage = truncateRunes(base, maxStoredURLLen)

	targets := p.targets()
	q := u.Query()

	// Ordena as chaves para que o resultado seja determinístico quando a
	// mesma chave aparece com caixas diferentes (UTM_Source e utm_source).
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	kept := url.Values{}
	for _, k := range keys {
		key := strings.ToLower(k)
		dst, ok := targets[key]
		if !ok || *dst != "" {
			continue
		}
		for _, v := range q[k] {
			if val, ok := sanitizeValue(key, v); ok {
				*dst = val
				kept.Set(key, val)
				break
			}
		}
	}

	page := base
	if len(kept) > 0 {
		page += "?" + kept.Encode()
	}
	p.PageURL = truncateRunes(page, maxStoredURLLen)
}

func (p *Params) applyReferrer(raw string, opts Options) {
	u, ok := parseHTTPURL(raw)
	if !ok {
		return
	}
	host := strings.ToLower(u.Hostname())
	// Sem query: referrers de buscadores podem carregar termos pesquisados.
	p.Referrer = truncateRunes(u.Scheme+"://"+strings.ToLower(u.Host)+u.EscapedPath(), maxStoredURLLen)
	p.ReferrerHost = host
	p.IgnoredReferrer = matchesAny(host, opts.OwnHosts) ||
		matchesAny(host, defaultIgnoredReferrerHosts) ||
		matchesAny(host, opts.IgnoredReferrerHosts)
}

// parseHTTPURL aceita só URLs absolutas http(s) com host e tamanho razoável.
func parseHTTPURL(raw string) (*url.URL, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxInputLen {
		return nil, false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, false
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, false
	}
	return u, true
}

func sanitizeValue(key, v string) (string, bool) {
	if strings.HasPrefix(key, "utm_") {
		v = cleanText(v)
		return v, v != ""
	}
	v = strings.TrimSpace(v)
	if v == "" || len(v) > maxClickIDLen || !clickIDPattern.MatchString(v) {
		return "", false
	}
	return v, true
}

// cleanText normaliza utm_*: remove caracteres de controle, apara espaços,
// converte para minúsculas (evita fragmentar relatórios) e limita o tamanho.
func cleanText(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.ToLower(strings.TrimSpace(s))
	return truncateRunes(s, maxTextLen)
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// ---------------------------------------------------------------------------
// Classificação de canal
// ---------------------------------------------------------------------------

var (
	paidMediums = map[string]bool{
		"cpc": true, "ppc": true, "cpm": true, "cpv": true, "display": true,
		"retargeting": true, "remarketing": true,
	}
	organicMediums = map[string]bool{"organic": true, "seo": true}
	socialMediums  = map[string]bool{"social": true, "social-network": true, "social_network": true, "sm": true}
	emailMediums   = map[string]bool{"email": true, "e-mail": true, "newsletter": true}

	socialSources = map[string]bool{
		"facebook": true, "fb": true, "instagram": true, "ig": true, "meta": true,
		"tiktok": true, "linkedin": true, "twitter": true, "x": true, "pinterest": true,
	}
	searchSources = map[string]bool{
		"google": true, "bing": true, "yahoo": true, "duckduckgo": true,
		"baidu": true, "yandex": true, "ecosia": true,
	}

	searchHostSuffixes = []string{
		"bing.com", "duckduckgo.com", "yahoo.com", "ecosia.org", "baidu.com",
		"yandex.com", "yandex.ru", "brave.com", "startpage.com", "qwant.com",
	}
	socialHostSuffixes = []string{
		"facebook.com", "fb.com", "instagram.com", "t.co", "twitter.com", "x.com",
		"linkedin.com", "lnkd.in", "tiktok.com", "youtube.com", "pinterest.com",
		"reddit.com", "whatsapp.com", "wa.me",
	}
	// Gateways de pagamento: o retorno do checkout não é uma origem de tráfego.
	defaultIgnoredReferrerHosts = []string{
		"paypal.com", "mercadopago.com.br", "mercadopago.com", "pagseguro.uol.com.br",
		"pagbank.com.br", "stripe.com", "pagar.me", "asaas.com", "ebanx.com",
	}

	googleHostPattern = regexp.MustCompile(`^(www\.)?google\.[a-z]{2,3}(\.[a-z]{2})?$`)
)

func isPaidMedium(m string) bool {
	return paidMediums[m] || strings.HasPrefix(m, "paid")
}

func classify(p Params) string {
	medium, source := p.UTMMedium, p.UTMSource

	// 1) UTM declarando mídia paga.
	if isPaidMedium(medium) {
		switch {
		case socialSources[source]:
			return ChannelPaidSocial
		case searchSources[source] || p.GCLID != "" || p.GBRAID != "" || p.WBRAID != "" || p.MSCLKID != "":
			return ChannelPaidSearch
		default:
			return ChannelPaidOther
		}
	}

	// 2) Click IDs de anúncio (fbclid fica de fora: também é anexado em
	// cliques orgânicos vindos do Facebook).
	switch {
	case p.GCLID != "" || p.GBRAID != "" || p.WBRAID != "" || p.MSCLKID != "":
		return ChannelPaidSearch
	case p.TTCLID != "" || p.LIFATID != "":
		return ChannelPaidSocial
	case p.DCLID != "":
		return ChannelPaidOther
	}

	// 3) Demais UTMs.
	switch {
	case emailMediums[medium]:
		return ChannelEmail
	case organicMediums[medium]:
		return ChannelOrganicSearch
	case socialMediums[medium]:
		return ChannelOrganicSocial
	}

	// 4) Referrer.
	if p.ReferrerHost != "" && !p.IgnoredReferrer {
		switch {
		case isSearchHost(p.ReferrerHost):
			return ChannelOrganicSearch
		case matchesAny(p.ReferrerHost, socialHostSuffixes):
			return ChannelOrganicSocial
		default:
			return ChannelReferral
		}
	}

	if p.FBCLID != "" {
		return ChannelOrganicSocial
	}
	return ChannelDirect
}

func isSearchHost(host string) bool {
	return googleHostPattern.MatchString(host) || matchesAny(host, searchHostSuffixes)
}

func matchesAny(host string, domains []string) bool {
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}
