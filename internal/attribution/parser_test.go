package attribution

import (
	"strings"
	"testing"
)

func TestParseURL_SanitizesPageURL(t *testing.T) {
	p := ParseURL("https://Example.com/lp?utm_source=Google&email=a%40b.com&gclid=Abc_123&x=1#utm_medium=frag", "")

	if p.LandingPage != "https://example.com/lp" {
		t.Errorf("LandingPage = %q", p.LandingPage)
	}
	if want := "https://example.com/lp?gclid=Abc_123&utm_source=google"; p.PageURL != want {
		t.Errorf("PageURL = %q, want %q", p.PageURL, want)
	}
	if p.UTMSource != "google" {
		t.Errorf("UTMSource = %q, want lowercase", p.UTMSource)
	}
	if p.GCLID != "Abc_123" {
		t.Errorf("GCLID = %q, caixa deve ser preservada", p.GCLID)
	}
	if p.UTMMedium != "" {
		t.Errorf("fragmento não deve ser lido, UTMMedium = %q", p.UTMMedium)
	}
	if strings.Contains(p.PageURL, "email") {
		t.Errorf("PageURL vazou parâmetro pessoal: %q", p.PageURL)
	}
}

func TestParseURL_UserinfoRemoved(t *testing.T) {
	p := ParseURL("https://user:pass@example.com/lp?utm_source=x", "")
	if strings.Contains(p.LandingPage, "user") || strings.Contains(p.PageURL, "pass") {
		t.Errorf("credenciais vazaram: %q / %q", p.LandingPage, p.PageURL)
	}
}

func TestParseURL_InvalidInputs(t *testing.T) {
	huge := "https://example.com/?utm_source=" + strings.Repeat("a", 9000)
	cases := map[string]string{
		"vazia":      "",
		"relativa":   "/lp?utm_source=x",
		"sem host":   "https:///lp?utm_source=x",
		"esquema":    "javascript:alert(1)",
		"gigante":    huge,
		"malformada": "http://[::1",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			p := ParseURL(raw, "")
			if p.LandingPage != "" || p.PageURL != "" || p.UTMSource != "" {
				t.Errorf("esperava Params vazio, obteve %+v", p)
			}
			if p.Channel != ChannelDirect {
				t.Errorf("Channel = %q, want direct", p.Channel)
			}
		})
	}
}

func TestParseURL_ClickIDValidation(t *testing.T) {
	p := ParseURL("https://example.com/?gclid=ab%3Cc&fbclid=&ttclid=Tt-9_x&msclkid="+strings.Repeat("a", 600), "")
	if p.GCLID != "" {
		t.Errorf("gclid inválido deveria ser descartado: %q", p.GCLID)
	}
	if p.FBCLID != "" {
		t.Errorf("fbclid vazio deveria continuar vazio: %q", p.FBCLID)
	}
	if p.TTCLID != "Tt-9_x" {
		t.Errorf("TTCLID = %q", p.TTCLID)
	}
	if p.MSCLKID != "" {
		t.Errorf("msclkid longo demais deveria ser descartado")
	}
	if strings.Contains(p.PageURL, "gclid") {
		t.Errorf("click ID descartado não pode ficar no PageURL: %q", p.PageURL)
	}
}

func TestParseURL_UTMLimitsAndCleaning(t *testing.T) {
	long := strings.Repeat("a", 300)
	p := ParseURL("https://example.com/?utm_campaign="+long+"&utm_term=%20Tênis%0A%20", "")
	if len(p.UTMCampaign) != 256 {
		t.Errorf("len(UTMCampaign) = %d, want 256", len(p.UTMCampaign))
	}
	if p.UTMTerm != "tênis" {
		t.Errorf("UTMTerm = %q", p.UTMTerm)
	}
}

func TestParseURL_CaseInsensitiveKeys(t *testing.T) {
	p := ParseURL("https://example.com/?UTM_Source=Meta&GCLID=Xyz", "")
	if p.UTMSource != "meta" || p.GCLID != "Xyz" {
		t.Errorf("chaves em caixa diferente deveriam casar: %+v", p)
	}
}

func TestParseURL_NewParams(t *testing.T) {
	p := ParseURL("https://example.com/?li_fat_id=abc-123&dclid=D1&gad_source=1&gad_campaignid=999", "")
	if p.LIFATID != "abc-123" || p.DCLID != "D1" || p.GadSource != "1" || p.GadCampaignID != "999" {
		t.Errorf("novos parâmetros não extraídos: %+v", p)
	}
	if !p.HasClickID() {
		t.Error("HasClickID deveria ser true")
	}
	if (Params{UTMSource: "x"}).HasClickID() {
		t.Error("HasClickID deveria ser false sem click ID")
	}
}

func TestParseURL_ReferrerStripsQuery(t *testing.T) {
	p := ParseURL("https://example.com/", "https://www.google.com/search?q=joao+silva+cpf")
	if p.Referrer != "https://www.google.com/search" {
		t.Errorf("Referrer = %q", p.Referrer)
	}
	if p.ReferrerHost != "www.google.com" {
		t.Errorf("ReferrerHost = %q", p.ReferrerHost)
	}
}

func TestParseURL_Channel(t *testing.T) {
	opts := Options{OwnHosts: []string{"mysite.com.br"}}
	cases := []struct {
		name     string
		page     string
		referrer string
		want     string
		ignored  bool
	}{
		{"gclid", "https://s.com/?gclid=abc", "", ChannelPaidSearch, false},
		{"cpc facebook", "https://s.com/?utm_medium=cpc&utm_source=facebook", "", ChannelPaidSocial, false},
		{"cpc google", "https://s.com/?utm_medium=cpc&utm_source=google", "", ChannelPaidSearch, false},
		{"cpc outro", "https://s.com/?utm_medium=cpc&utm_source=parceiro", "", ChannelPaidOther, false},
		{"paid_social prefixo", "https://s.com/?utm_medium=paid_social&utm_source=tiktok", "", ChannelPaidSocial, false},
		{"ttclid", "https://s.com/?ttclid=abc", "", ChannelPaidSocial, false},
		{"li_fat_id", "https://s.com/?li_fat_id=abc", "", ChannelPaidSocial, false},
		{"dclid", "https://s.com/?dclid=abc", "", ChannelPaidOther, false},
		{"email", "https://s.com/?utm_medium=email", "", ChannelEmail, false},
		{"organic utm", "https://s.com/?utm_medium=organic&utm_source=google", "", ChannelOrganicSearch, false},
		{"social utm", "https://s.com/?utm_medium=social", "", ChannelOrganicSocial, false},
		{"google br", "https://s.com/", "https://www.google.com.br/", ChannelOrganicSearch, false},
		{"bing", "https://s.com/", "https://www.bing.com/search?q=x", ChannelOrganicSearch, false},
		{"instagram", "https://s.com/", "https://l.instagram.com/?u=x", ChannelOrganicSocial, false},
		{"referral", "https://s.com/", "https://blog.example.org/post", ChannelReferral, false},
		{"proprio site", "https://mysite.com.br/", "https://www.mysite.com.br/outra", ChannelDirect, true},
		{"gateway", "https://mysite.com.br/obrigado", "https://www.paypal.com/checkout", ChannelDirect, true},
		{"fbclid sozinho", "https://s.com/?fbclid=abc", "", ChannelOrganicSocial, false},
		{"direto", "https://s.com/", "", ChannelDirect, false},
		{"gclid vence referrer", "https://s.com/?gclid=abc", "https://blog.example.org/", ChannelPaidSearch, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := ParseURLWithOptions(tc.page, tc.referrer, opts)
			if p.Channel != tc.want {
				t.Errorf("Channel = %q, want %q", p.Channel, tc.want)
			}
			if p.IgnoredReferrer != tc.ignored {
				t.Errorf("IgnoredReferrer = %v, want %v", p.IgnoredReferrer, tc.ignored)
			}
		})
	}
}

func TestMatchesAny_NoPartialSuffix(t *testing.T) {
	if matchesAny("notgoogle.com", []string{"google.com"}) {
		t.Error("notgoogle.com não deve casar com google.com")
	}
	if !matchesAny("l.instagram.com", []string{"instagram.com"}) {
		t.Error("subdomínio deveria casar")
	}
}

func TestParseURL_CanonicalDimensionsAndGA4Conventions(t *testing.T) {
	tests := []struct {
		name        string
		rawURL      string
		rawReferrer string
		wantSource  string
		wantMedium  string
		wantChannel string
		wantBasis   string
	}{
		{
			name:        "Caso Real SPS Power (GCLID + Campanha sem utm_source)",
			rawURL:      "https://spspower.com.br/?gad_campaignid=18939184497&gad_source=1&gbraid=0AAAAABD0k6t&gclid=EAIaIQob_123&utm_campaign=Search_Info_sps&utm_term=nobreak",
			rawReferrer: "",
			wantSource:  "google",
			wantMedium:  "cpc",
			wantChannel: ChannelPaidSearch,
			wantBasis:   BasisClickID,
		},
		{
			name:        "Aliases de Origem Meta (fb, ig)",
			rawURL:      "https://example.com/?utm_source=FB&utm_medium=CPC",
			rawReferrer: "",
			wantSource:  "meta",
			wantMedium:  "cpc",
			wantChannel: ChannelPaidSocial,
			wantBasis:   BasisUTM,
		},
		{
			name:        "Aliases de Origem Microsoft (bingads)",
			rawURL:      "https://example.com/?utm_source=BingAds&utm_medium=cpc",
			rawReferrer: "",
			wantSource:  "microsoft",
			wantMedium:  "cpc",
			wantChannel: ChannelPaidSearch,
			wantBasis:   BasisUTM,
		},
		{
			name:        "Campanha Órfã (utm_campaign sem utm_source nem click ID)",
			rawURL:      "https://example.com/?utm_campaign=BlackFriday2026",
			rawReferrer: "",
			wantSource:  "not_set",
			wantMedium:  "not_set",
			wantChannel: ChannelNotSet,
			wantBasis:   BasisUTM,
		},
		{
			name:        "UTM Desconhecida (GA4 Unassigned -> other, nunca direct)",
			rawURL:      "https://example.com/?utm_source=afiliado_x&utm_medium=banner_customizado",
			rawReferrer: "",
			wantSource:  "afiliado_x",
			wantMedium:  "banner_customizado",
			wantChannel: ChannelOther,
			wantBasis:   BasisUTM,
		},
		{
			name:        "FBCLID Isolado (Conservador: Social Orgânico, não pago)",
			rawURL:      "https://example.com/?fbclid=IwAR3XYZ",
			rawReferrer: "",
			wantSource:  "meta",
			wantMedium:  "social",
			wantChannel: ChannelOrganicSocial,
			wantBasis:   BasisClickID,
		},
		{
			name:        "TikTok Click ID Isolado",
			rawURL:      "https://example.com/?ttclid=TT123",
			rawReferrer: "",
			wantSource:  "tiktok",
			wantMedium:  "cpc",
			wantChannel: ChannelPaidSocial,
			wantBasis:   BasisClickID,
		},
		{
			name:        "Referrer Orgânico do Google",
			rawURL:      "https://example.com/produtos",
			rawReferrer: "https://www.google.com.br/search?q=nobreak",
			wantSource:  "google",
			wantMedium:  "organic",
			wantChannel: ChannelOrganicSearch,
			wantBasis:   BasisReferrer,
		},
		{
			name:        "Acesso Direto",
			rawURL:      "https://example.com/",
			rawReferrer: "",
			wantSource:  "direct",
			wantMedium:  "none",
			wantChannel: ChannelDirect,
			wantBasis:   BasisNone,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := ParseURL(tc.rawURL, tc.rawReferrer)
			if p.Source != tc.wantSource {
				t.Errorf("Source = %q, want %q", p.Source, tc.wantSource)
			}
			if p.Medium != tc.wantMedium {
				t.Errorf("Medium = %q, want %q", p.Medium, tc.wantMedium)
			}
			if p.Channel != tc.wantChannel {
				t.Errorf("Channel = %q, want %q", p.Channel, tc.wantChannel)
			}
			if p.AttributionBasis != tc.wantBasis {
				t.Errorf("AttributionBasis = %q, want %q", p.AttributionBasis, tc.wantBasis)
			}
		})
	}
}

