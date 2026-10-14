package collector

import (
	"net/url"
	"path"
	"sort"
	"strings"
)

// CanonicalSettings define as regras de normalização de URLs e consolidação por site
type CanonicalSettings struct {
	CanonicalHost        string   `json:"canonical_host"`         // Host canônico preferencial (ex: "spspower.com.br" ou "www.spspower.com.br")
	HostAliases          []string `json:"host_aliases"`          // Aliases que devem ser consolidados no host canônico (ex: ["www.spspower.com.br"])
	QueryParamAllowlist  []string `json:"query_param_allowlist"` // Parâmetros estruturais permitidos (ex: ["p", "produto", "id"])
	EnforceTrailingSlash bool     `json:"enforce_trailing_slash"` // Adiciona barra final em caminhos sem extensão (default: true)
}

// DefaultCanonicalSettings inicializa as regras canônicas padrão para o domínio informado
func DefaultCanonicalSettings(primaryDomain string) *CanonicalSettings {
	clean := cleanHost(primaryDomain)
	aliases := make([]string, 0)
	if clean != "" {
		if strings.HasPrefix(clean, "www.") {
			apex := strings.TrimPrefix(clean, "www.")
			if apex != "" {
				aliases = append(aliases, apex)
			}
		} else {
			aliases = append(aliases, "www."+clean)
		}
	}
	return &CanonicalSettings{
		CanonicalHost:        clean,
		HostAliases:          aliases,
		QueryParamAllowlist:  []string{},
		EnforceTrailingSlash: true,
	}
}

// IsAuthorizedDomain verifica se um determinado host pertence ao site (canônico, alias ou whitelist)
func (s *CanonicalSettings) IsAuthorizedDomain(host string, additionalAllowedDomains ...string) bool {
	h := cleanHost(host)
	if h == "" {
		return false
	}
	if s.CanonicalHost != "" && strings.EqualFold(h, s.CanonicalHost) {
		return true
	}
	for _, alias := range s.HostAliases {
		if strings.EqualFold(h, cleanHost(alias)) {
			return true
		}
	}
	for _, allowed := range additionalAllowedDomains {
		if strings.EqualFold(h, cleanHost(allowed)) {
			return true
		}
	}
	return false
}

// NormalizeURL aplica as regras de canonicidade sobre uma URL, retornando o page_key normalizado
func (s *CanonicalSettings) NormalizeURL(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}

	// Remove fragmento de âncora (#) imediatamente
	if idx := strings.Index(trimmed, "#"); idx != -1 {
		trimmed = trimmed[:idx]
	}

	// Se não tiver esquema, assume https:// para parsing robusto
	parseTarget := trimmed
	if !strings.Contains(parseTarget, "://") {
		parseTarget = "https://" + strings.TrimLeft(parseTarget, "/")
	}

	u, err := url.Parse(parseTarget)
	if err != nil {
		return trimmed
	}

	// 1. Host normalizado em minúsculas
	rawHost := u.Hostname()
	cleanH := cleanHost(rawHost)
	if cleanH == "" {
		cleanH = strings.ToLower(strings.TrimSpace(rawHost))
	}

	finalHost := cleanH
	if s.CanonicalHost != "" {
		canonicalClean := cleanHost(s.CanonicalHost)
		if strings.EqualFold(cleanH, canonicalClean) {
			finalHost = canonicalClean
		} else {
			// Verifica se o host é um alias configurado
			isAlias := false
			for _, alias := range s.HostAliases {
				if strings.EqualFold(cleanH, cleanHost(alias)) {
					isAlias = true
					break
				}
			}
			if isAlias {
				finalHost = canonicalClean
			}
			// Caso contrário (ex: blog.spspower.com.br não configurado em aliases), mantém cleanH isolado
		}
	}

	// 2. Caminho (Path) preservando a caixa original, saneando index e barras repetidas
	rawPath := u.Path
	if rawPath == "" {
		rawPath = "/"
	}

	// Normaliza barras repetidas // -> /
	for strings.Contains(rawPath, "//") {
		rawPath = strings.ReplaceAll(rawPath, "//", "/")
	}

	// Remoção de arquivos de índice padrão
	lowerPath := strings.ToLower(rawPath)
	if strings.HasSuffix(lowerPath, "/index.html") {
		rawPath = rawPath[:len(rawPath)-len("index.html")]
	} else if strings.HasSuffix(lowerPath, "/index.htm") {
		rawPath = rawPath[:len(rawPath)-len("index.htm")]
	} else if strings.HasSuffix(lowerPath, "/index.php") {
		rawPath = rawPath[:len(rawPath)-len("index.php")]
	} else if lowerPath == "index.html" || lowerPath == "index.htm" || lowerPath == "index.php" {
		rawPath = "/"
	}

	if rawPath == "" {
		rawPath = "/"
	}

	// Barra final condicional:
	// Apenas aplicada se o caminho NÃO possuir extensão de arquivo identificável (ex: /arquivo.pdf fica inalterado)
	fileExt := path.Ext(rawPath)
	if fileExt == "" {
		if s.EnforceTrailingSlash {
			if !strings.HasSuffix(rawPath, "/") {
				rawPath += "/"
			}
		} else {
			if rawPath != "/" && strings.HasSuffix(rawPath, "/") {
				rawPath = strings.TrimSuffix(rawPath, "/")
			}
		}
	}

	// 3. Filtragem de Query String com allowlist por site
	rawQuery := u.RawQuery
	var finalQuery string
	if rawQuery != "" && len(s.QueryParamAllowlist) > 0 {
		values, errQ := url.ParseQuery(rawQuery)
		if errQ == nil {
			allowedMap := make(map[string]bool)
			for _, p := range s.QueryParamAllowlist {
				allowedMap[strings.ToLower(strings.TrimSpace(p))] = true
			}

			allowedValues := make(url.Values)
			keys := make([]string, 0)
			for k, vList := range values {
				if allowedMap[strings.ToLower(k)] {
					allowedValues[k] = vList
					keys = append(keys, k)
				}
			}

			if len(keys) > 0 {
				sort.Strings(keys)
				var qParts []string
				for _, k := range keys {
					for _, val := range allowedValues[k] {
						qParts = append(qParts, url.QueryEscape(k)+"="+url.QueryEscape(val))
					}
				}
				finalQuery = strings.Join(qParts, "&")
			}
		}
	}

	// 4. Montagem da URL canônica final (sempre scheme https://)
	var sb strings.Builder
	sb.WriteString("https://")
	sb.WriteString(finalHost)
	if !strings.HasPrefix(rawPath, "/") {
		sb.WriteString("/")
	}
	sb.WriteString(rawPath)
	if finalQuery != "" {
		sb.WriteString("?")
		sb.WriteString(finalQuery)
	}

	return sb.String()
}

// ValidateAndResolveCanonical valida a canonical_url informada pelo SDK.
// Se a URL canônica pertencer ao site (host canônico, aliases ou domínios autorizados), utiliza-a para normalização.
// Caso contrário (ex: terceiro malicioso, link de parceiro), descarta e normaliza a URL da requisição.
func (s *CanonicalSettings) ValidateAndResolveCanonical(requestURL, canonicalURL string, additionalAllowedDomains ...string) string {
	canTrimmed := strings.TrimSpace(canonicalURL)
	if canTrimmed != "" {
		// Remove fragmento antes de analisar
		if idx := strings.Index(canTrimmed, "#"); idx != -1 {
			canTrimmed = canTrimmed[:idx]
		}
		parseTarget := canTrimmed
		if !strings.Contains(parseTarget, "://") {
			parseTarget = "https://" + strings.TrimLeft(parseTarget, "/")
		}
		u, err := url.Parse(parseTarget)
		if err == nil {
			host := u.Hostname()
			if s.IsAuthorizedDomain(host, additionalAllowedDomains...) {
				return s.NormalizeURL(canTrimmed)
			}
		}
	}

	return s.NormalizeURL(requestURL)
}
