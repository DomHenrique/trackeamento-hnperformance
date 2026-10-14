package sitemap

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// IsPrivateOrForbiddenIP verifica se o endereço IP pertence a faixas privadas, loopback, link-local ou metadados de nuvem
func IsPrivateOrForbiddenIP(ip net.IP) bool {
	if ip == nil {
		return true
	}

	// 1. Loopback (127.0.0.0/8, ::1)
	if ip.IsLoopback() {
		return true
	}

	// 2. Não especificado (0.0.0.0, ::)
	if ip.IsUnspecified() {
		return true
	}

	// 3. Redes privadas RFC 1918 (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, fc00::/7)
	if ip.IsPrivate() {
		return true
	}

	// 4. Link-Local (169.254.0.0/16, fe80::/10) - inclui provedores de metadados em nuvem (169.254.169.254)
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}

	// 5. Teste explícito para IPv4 mapeado em IPv6
	if ipv4 := ip.To4(); ipv4 != nil {
		if ipv4[0] == 127 || ipv4[0] == 0 {
			return true
		}
		if ipv4[0] == 10 {
			return true
		}
		if ipv4[0] == 172 && (ipv4[1] >= 16 && ipv4[1] <= 31) {
			return true
		}
		if ipv4[0] == 192 && ipv4[1] == 168 {
			return true
		}
		if ipv4[0] == 169 && ipv4[1] == 254 {
			return true
		}
		// Carrier-grade NAT (100.64.0.0/10)
		if ipv4[0] == 100 && (ipv4[1] >= 64 && ipv4[1] <= 127) {
			return true
		}
	}

	return false
}

// IsHostAllowed verifica se o host da URL pertence à lista de domínios autorizados do site
func IsHostAllowed(rawURL string, allowedHosts []string) (bool, string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return false, "", fmt.Errorf("url vazia")
	}

	u, err := url.Parse(trimmed)
	if err != nil {
		return false, "", fmt.Errorf("url malformatada: %w", err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return false, "", fmt.Errorf("esquema '%s' nao permitido (apenas http/https)", scheme)
	}

	reqHost := strings.ToLower(u.Hostname())
	if reqHost == "" {
		return false, "", fmt.Errorf("host ausente na url")
	}

	// Compara contra lista de domínios permitidos (case-insensitive)
	for _, h := range allowedHosts {
		clean := strings.ToLower(strings.TrimSpace(h))
		if clean == "" {
			continue
		}
		if reqHost == clean {
			return true, reqHost, nil
		}
	}

	return false, reqHost, fmt.Errorf("host '%s' nao pertence aos dominios autorizados do site", reqHost)
}

// NewSafeHTTPClient cria um cliente HTTP com proteção anti-SSRF em nível de dialing de socket e restrição de domínios
func NewSafeHTTPClient(allowedHosts []string, timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}

	dialer := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 15 * time.Second,
		Control: func(network, address string, c syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				host = address
			}

			ip := net.ParseIP(host)
			if ip != nil && IsPrivateOrForbiddenIP(ip) {
				return fmt.Errorf("conexao bloqueada por politica anti-SSRF: endereco ip '%s' e privado ou restrito", host)
			}
			return nil
		},
	}

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("limite de 5 redirecionamentos excedido")
			}
			// Valida se o destino do redirect continua em um domínio autorizado
			allowed, _, err := IsHostAllowed(req.URL.String(), allowedHosts)
			if !allowed || err != nil {
				return fmt.Errorf("redirecionamento bloqueado por politica anti-SSRF: %v", err)
			}
			return nil
		},
	}

	return client
}

// SafeFetch executa uma requisição GET segura para a URL informada respeitando timeouts e limites
func SafeFetch(ctx context.Context, client *http.Client, targetURL string, allowedHosts []string) (*http.Response, error) {
	// Validação preliminar do host antes de qualquer chamada
	allowed, _, err := IsHostAllowed(targetURL, allowedHosts)
	if !allowed || err != nil {
		return nil, fmt.Errorf("url invalida: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, fmt.Errorf("erro ao criar requisicao: %w", err)
	}

	req.Header.Set("User-Agent", "HN-Tracking-Engine/1.0 (Sitemap Monitor; +https://hnperformancedigital.com.br)")
	req.Header.Set("Accept", "application/xml, text/xml, */*")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("falha ao requisitar sitemap: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("servidor remoto retornou status http %d", resp.StatusCode)
	}

	return resp, nil
}
