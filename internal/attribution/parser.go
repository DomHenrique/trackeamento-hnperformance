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
	ChannelOther         = "other"
	ChannelNotSet        = "not_set"
)

// Bases de atribuição retornadas em Params.AttributionBasis.
const (
	BasisUTM      = "utm"
	BasisClickID  = "click_id"
	BasisReferrer = "referrer"
	BasisNone     = "none"
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

	// Dimensões Canônicas Derivadas (Convenção GA4)
	Source           string `json:"source,omitempty"`
	Medium           string `json:"medium,omitempty"`
	Channel          string `json:"channel,omitempty"`
	AttributionBasis string `json:"attribution_basis,omitempty"`
	Inherited        uint8  `json:"inherited,omitempty"`

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
	classify(&p)
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
// Classificação de canal e normalização de dimensões canônicas (GA4)
// ---------------------------------------------------------------------------

var (
	paidMediums = map[string]bool{
		"cpc": true, "ppc": true, "cpm": true, "cpv": true, "display": true,
		"retargeting": true, "remarketing": true, "paid": true,
	}
	organicMediums = map[string]bool{"organic": true, "seo": true}
	socialMediums  = map[string]bool{"social": true, "social-network": true, "social_network": true, "sm": true}
	emailMediums   = map[string]bool{"email": true, "e-mail": true, "newsletter": true}

	socialSources = map[string]bool{
		"facebook": true, "fb": true, "instagram": true, "ig": true, "meta": true,
		"tiktok": true, "linkedin": true, "twitter": true, "x": true, "pinterest": true,
		"reddit": true, "whatsapp": true,
	}
	searchSources = map[string]bool{
		"google": true, "bing": true, "yahoo": true, "duckduckgo": true,
		"baidu": true, "yandex": true, "ecosia": true, "microsoft": true,
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

	// Dicionário unificado de aliases canônicos (aplicado pós-lowercase)
	canonicalSourceAliases = map[string]string{
		"fb": "meta", "facebook": "meta", "ig": "meta", "instagram": "meta", "meta": "meta",
		"adwords": "google", "googleads": "google", "gads": "google", "google": "google",
		"bing": "microsoft", "bingads": "microsoft", "msn": "microsoft", "microsoft": "microsoft",
		"tiktok": "tiktok", "tt": "tiktok", "bytedance": "tiktok",
		"linkedin": "linkedin", "lnkd": "linkedin",
		"twitter": "twitter", "x": "twitter", "t.co": "twitter",
		"pinterest": "pinterest", "pin": "pinterest",
		"whatsapp": "whatsapp", "wa": "whatsapp",
	}
)

func isPaidMedium(m string) bool {
	return paidMediums[m] || strings.HasPrefix(m, "paid")
}

func normalizeSource(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if alias, ok := canonicalSourceAliases[s]; ok {
		return alias
	}
	return s
}

func resolveSearchEngine(host string) string {
	if googleHostPattern.MatchString(host) {
		return "google"
	}
	for _, sfx := range searchHostSuffixes {
		if host == sfx || strings.HasSuffix(host, "."+sfx) {
			name := strings.TrimSuffix(sfx, ".com")
			name = strings.TrimSuffix(name, ".org")
			name = strings.TrimSuffix(name, ".ru")
			return normalizeSource(name)
		}
	}
	return "google"
}

func resolveSocialNetwork(host string) string {
	for _, sfx := range socialHostSuffixes {
		if host == sfx || strings.HasSuffix(host, "."+sfx) {
			name := strings.TrimSuffix(sfx, ".com")
			name = strings.TrimSuffix(name, ".in")
			name = strings.TrimSuffix(name, ".me")
			return normalizeSource(name)
		}
	}
	return "meta"
}

func classify(p *Params) {
	// 1) Caso de Campanha Órfã: utm_campaign preenchido sem utm_source e sem click ID
	if p.UTMSource == "" && !p.HasClickID() && p.UTMCampaign != "" {
		p.Source = "not_set"
		p.Medium = "not_set"
		p.Channel = ChannelNotSet
		p.AttributionBasis = BasisUTM
		return
	}

	// 2) UTM Declarado (Source ou Medium preenchidos)
	if p.UTMSource != "" || p.UTMMedium != "" {
		p.AttributionBasis = BasisUTM
		p.Source = normalizeSource(p.UTMSource)
		if p.Source == "" {
			p.Source = "not_set"
		}
		p.Medium = strings.ToLower(strings.TrimSpace(p.UTMMedium))
		if p.Medium == "" {
			p.Medium = "not_set"
		}

		switch {
		case isPaidMedium(p.Medium):
			switch {
			case socialSources[p.Source] || p.Source == "meta" || p.TTCLID != "" || p.LIFATID != "":
				p.Channel = ChannelPaidSocial
			case searchSources[p.Source] || p.Source == "microsoft" || p.GCLID != "" || p.GBRAID != "" || p.WBRAID != "" || p.MSCLKID != "":
				p.Channel = ChannelPaidSearch
			default:
				p.Channel = ChannelPaidOther
			}
		case emailMediums[p.Medium]:
			p.Channel = ChannelEmail
		case organicMediums[p.Medium]:
			p.Channel = ChannelOrganicSearch
		case socialMediums[p.Medium]:
			p.Channel = ChannelOrganicSocial
		default:
			// Regra GA4 Unassigned: UTMs desconhecidas viram "other", nunca "direct"!
			p.Channel = ChannelOther
		}
		return
	}

	// 3) Click IDs Oficiais (Sem UTMs declaradas)
	if p.GCLID != "" || p.GBRAID != "" || p.WBRAID != "" {
		p.Source = "google"
		p.Medium = "cpc"
		p.Channel = ChannelPaidSearch
		p.AttributionBasis = BasisClickID
		return
	}
	if p.MSCLKID != "" {
		p.Source = "microsoft"
		p.Medium = "cpc"
		p.Channel = ChannelPaidSearch
		p.AttributionBasis = BasisClickID
		return
	}
	if p.TTCLID != "" {
		p.Source = "tiktok"
		p.Medium = "cpc"
		p.Channel = ChannelPaidSocial
		p.AttributionBasis = BasisClickID
		return
	}
	if p.LIFATID != "" {
		p.Source = "linkedin"
		p.Medium = "cpc"
		p.Channel = ChannelPaidSocial
		p.AttributionBasis = BasisClickID
		return
	}
	if p.DCLID != "" {
		p.Source = "google"
		p.Medium = "cpm"
		p.Channel = ChannelPaidOther
		p.AttributionBasis = BasisClickID
		return
	}
	if p.FBCLID != "" {
		// fbclid isolado não comprova mídia paga (anexado também em posts/mensagens orgânicas)
		p.Source = "meta"
		p.Medium = "social"
		p.Channel = ChannelOrganicSocial
		p.AttributionBasis = BasisClickID
		return
	}

	// 4) Referrer (Sem UTMs e sem Click IDs)
	if p.ReferrerHost != "" && !p.IgnoredReferrer {
		p.AttributionBasis = BasisReferrer
		switch {
		case isSearchHost(p.ReferrerHost):
			p.Source = resolveSearchEngine(p.ReferrerHost)
			p.Medium = "organic"
			p.Channel = ChannelOrganicSearch
		case matchesAny(p.ReferrerHost, socialHostSuffixes):
			p.Source = resolveSocialNetwork(p.ReferrerHost)
			p.Medium = "referral"
			p.Channel = ChannelOrganicSocial
		default:
			p.Source = p.ReferrerHost
			p.Medium = "referral"
			p.Channel = ChannelReferral
		}
		return
	}

	// 5) Acesso Direto Puro
	p.Source = "direct"
	p.Medium = "none"
	p.Channel = ChannelDirect
	p.AttributionBasis = BasisNone
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
