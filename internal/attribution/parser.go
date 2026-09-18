package attribution

import (
	"net/url"
	"strings"
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
}

// ParseURL analisa a URL da página ou referrer e extrai os parâmetros de campanha e cliques de anúncios
func ParseURL(rawURL, rawReferrer string) Params {
	p := Params{
		PageURL:  rawURL,
		Referrer: rawReferrer,
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return p
	}

	// Landing Page limpa sem query strings caso necessário
	p.LandingPage = u.Scheme + "://" + u.Host + u.Path

	q := u.Query()
	p.UTMSource = strings.TrimSpace(q.Get("utm_source"))
	p.UTMMedium = strings.TrimSpace(q.Get("utm_medium"))
	p.UTMCampaign = strings.TrimSpace(q.Get("utm_campaign"))
	p.UTMContent = strings.TrimSpace(q.Get("utm_content"))
	p.UTMTerm = strings.TrimSpace(q.Get("utm_term"))

	p.GCLID = strings.TrimSpace(q.Get("gclid"))
	p.GBRAID = strings.TrimSpace(q.Get("gbraid"))
	p.WBRAID = strings.TrimSpace(q.Get("wbraid"))
	p.FBCLID = strings.TrimSpace(q.Get("fbclid"))
	p.TTCLID = strings.TrimSpace(q.Get("ttclid"))

	return p
}
