package attribution

import (
	"testing"
)

func TestParseURL(t *testing.T) {
	rawURL := "https://exemplo.com.br/orcamento?utm_source=google&utm_medium=cpc&utm_campaign=black_friday&gclid=Cj0KCQjww5u2BhC-ARIs&fbclid=IwAR2xyz"
	rawReferrer := "https://google.com/"

	params := ParseURL(rawURL, rawReferrer)

	if params.UTMSource != "google" {
		t.Errorf("esperado utm_source 'google', obtido '%s'", params.UTMSource)
	}
	if params.UTMMedium != "cpc" {
		t.Errorf("esperado utm_medium 'cpc', obtido '%s'", params.UTMMedium)
	}
	if params.UTMCampaign != "black_friday" {
		t.Errorf("esperado utm_campaign 'black_friday', obtido '%s'", params.UTMCampaign)
	}
	if params.GCLID != "Cj0KCQjww5u2BhC-ARIs" {
		t.Errorf("esperado gclid 'Cj0KCQjww5u2BhC-ARIs', obtido '%s'", params.GCLID)
	}
	if params.FBCLID != "IwAR2xyz" {
		t.Errorf("esperado fbclid 'IwAR2xyz', obtido '%s'", params.FBCLID)
	}
	if params.LandingPage != "https://exemplo.com.br/orcamento" {
		t.Errorf("esperado landing page limpa 'https://exemplo.com.br/orcamento', obtido '%s'", params.LandingPage)
	}
	if params.Referrer != rawReferrer {
		t.Errorf("esperado referrer '%s', obtido '%s'", rawReferrer, params.Referrer)
	}
}
