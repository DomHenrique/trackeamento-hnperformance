package collector

import (
	"testing"
)

func TestCanonicalSettings_NormalizeURL(t *testing.T) {
	tests := []struct {
		name     string
		settings *CanonicalSettings
		input    string
		expected string
	}{
		{
			name: "Consolida www no apex canônico com barra final",
			settings: &CanonicalSettings{
				CanonicalHost:        "spspower.com.br",
				HostAliases:          []string{"www.spspower.com.br"},
				QueryParamAllowlist:  []string{},
				EnforceTrailingSlash: true,
			},
			input:    "http://www.spspower.com.br/nobreaks",
			expected: "https://spspower.com.br/nobreaks/",
		},
		{
			name: "Consolida apex no www quando o site define www como canônico",
			settings: &CanonicalSettings{
				CanonicalHost:        "www.spspower.com.br",
				HostAliases:          []string{"spspower.com.br"},
				QueryParamAllowlist:  []string{},
				EnforceTrailingSlash: true,
			},
			input:    "http://spspower.com.br/nobreaks/",
			expected: "https://www.spspower.com.br/nobreaks/",
		},
		{
			name: "Preserva subdomínio isolado não configurado como alias",
			settings: &CanonicalSettings{
				CanonicalHost:        "spspower.com.br",
				HostAliases:          []string{"www.spspower.com.br"},
				QueryParamAllowlist:  []string{},
				EnforceTrailingSlash: true,
			},
			input:    "https://blog.spspower.com.br/artigo-tecnico",
			expected: "https://blog.spspower.com.br/artigo-tecnico/",
		},
		{
			name: "Preserva arquivos com extensão sem aplicar barra final",
			settings: &CanonicalSettings{
				CanonicalHost:        "spspower.com.br",
				HostAliases:          []string{"www.spspower.com.br"},
				QueryParamAllowlist:  []string{},
				EnforceTrailingSlash: true,
			},
			input:    "https://spspower.com.br/downloads/manual_usuario.pdf",
			expected: "https://spspower.com.br/downloads/manual_usuario.pdf",
		},
		{
			name: "Remove index.html e index.php",
			settings: &CanonicalSettings{
				CanonicalHost:        "spspower.com.br",
				HostAliases:          []string{"www.spspower.com.br"},
				QueryParamAllowlist:  []string{},
				EnforceTrailingSlash: true,
			},
			input:    "https://spspower.com.br/produtos/index.html",
			expected: "https://spspower.com.br/produtos/",
		},
		{
			name: "Remove index.php na raiz",
			settings: &CanonicalSettings{
				CanonicalHost:        "spspower.com.br",
				HostAliases:          []string{"www.spspower.com.br"},
				QueryParamAllowlist:  []string{},
				EnforceTrailingSlash: true,
			},
			input:    "https://spspower.com.br/index.php",
			expected: "https://spspower.com.br/",
		},
		{
			name: "Remove âncoras/fragmentos de hash",
			settings: &CanonicalSettings{
				CanonicalHost:        "spspower.com.br",
				HostAliases:          []string{"www.spspower.com.br"},
				QueryParamAllowlist:  []string{},
				EnforceTrailingSlash: true,
			},
			input:    "https://spspower.com.br/contato#formulario",
			expected: "https://spspower.com.br/contato/",
		},
		{
			name: "Descarta parâmetros de mídia quando allowlist estiver vazia",
			settings: &CanonicalSettings{
				CanonicalHost:        "spspower.com.br",
				HostAliases:          []string{"www.spspower.com.br"},
				QueryParamAllowlist:  []string{},
				EnforceTrailingSlash: true,
			},
			input:    "https://spspower.com.br/nobreaks/?utm_source=google&utm_medium=cpc&gclid=Cj0KCQj",
			expected: "https://spspower.com.br/nobreaks/",
		},
		{
			name: "Mantém apenas parâmetros permitidos na allowlist e ordena alfabeticamente",
			settings: &CanonicalSettings{
				CanonicalHost:        "spspower.com.br",
				HostAliases:          []string{"www.spspower.com.br"},
				QueryParamAllowlist:  []string{"p", "categoria"},
				EnforceTrailingSlash: true,
			},
			input:    "https://spspower.com.br/loja?utm_source=fb&categoria=solar&p=123&fbclid=XYZ",
			expected: "https://spspower.com.br/loja/?categoria=solar&p=123",
		},
		{
			name: "Normaliza barras duplas consecutivas",
			settings: &CanonicalSettings{
				CanonicalHost:        "spspower.com.br",
				HostAliases:          []string{"www.spspower.com.br"},
				QueryParamAllowlist:  []string{},
				EnforceTrailingSlash: true,
			},
			input:    "https://spspower.com.br//produtos///baterias",
			expected: "https://spspower.com.br/produtos/baterias/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.settings.NormalizeURL(tt.input)
			if got != tt.expected {
				t.Errorf("NormalizeURL(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestCanonicalSettings_ValidateAndResolveCanonical(t *testing.T) {
	settings := &CanonicalSettings{
		CanonicalHost:        "spspower.com.br",
		HostAliases:          []string{"www.spspower.com.br"},
		QueryParamAllowlist:  []string{"p"},
		EnforceTrailingSlash: true,
	}

	t.Run("Aceita canonical_url válida e autorizada do mesmo site", func(t *testing.T) {
		reqURL := "http://www.spspower.com.br/nobreaks?utm_source=google"
		canURL := "https://spspower.com.br/nobreaks/"
		got := settings.ValidateAndResolveCanonical(reqURL, canURL)
		expected := "https://spspower.com.br/nobreaks/"
		if got != expected {
			t.Errorf("got %q, want %q", got, expected)
		}
	})

	t.Run("Rejeita canonical_url externa maliciosa e faz fallback seguro para a URL da requisição", func(t *testing.T) {
		reqURL := "https://spspower.com.br/nobreaks"
		canURL := "https://malicious-site.com/phishing"
		got := settings.ValidateAndResolveCanonical(reqURL, canURL)
		expected := "https://spspower.com.br/nobreaks/"
		if got != expected {
			t.Errorf("got %q, want %q", got, expected)
		}
	})

	t.Run("Fallback seguro quando canonical_url estiver vazia", func(t *testing.T) {
		reqURL := "http://www.spspower.com.br/empresa"
		got := settings.ValidateAndResolveCanonical(reqURL, "")
		expected := "https://spspower.com.br/empresa/"
		if got != expected {
			t.Errorf("got %q, want %q", got, expected)
		}
	})
}
