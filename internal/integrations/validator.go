package integrations

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

var (
	// Redes proibidas de saída em ambientes de produção
	linkLocalMetadata = &net.IPNet{
		IP:   net.ParseIP("169.254.0.0"),
		Mask: net.CIDRMask(16, 32),
	}
	carrierGradeNAT = &net.IPNet{
		IP:   net.ParseIP("100.64.0.0"),
		Mask: net.CIDRMask(10, 32),
	}
	benchmarkTesting = &net.IPNet{
		IP:   net.ParseIP("198.18.0.0"),
		Mask: net.CIDRMask(15, 32),
	}
)

// ValidateOutboundURL valida URLs contra ataques de SSRF (Server-Side Request Forgery)
// rejeitando esquemas não seguros, loopback, RFC1918, link-local e metadados de nuvem.
// Respeita OUTBOUND_ALLOWED_HOSTS caso destinos de automação interna estejam autorizados.
func ValidateOutboundURL(rawURL string, env string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return errors.New("url de destino vazia")
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("url invalida: %w", err)
	}

	scheme := strings.ToLower(parsed.Scheme)
	isDevOrTest := env == "development" || env == "test"

	if !isDevOrTest {
		if scheme != "https" {
			return fmt.Errorf("esquema '%s' nao permitido: conexoes externas exigem https", scheme)
		}
	} else {
		if scheme != "http" && scheme != "https" {
			return fmt.Errorf("esquema '%s' nao suportado: deve ser http ou https", scheme)
		}
	}

	hostname := parsed.Hostname()
	if hostname == "" {
		return errors.New("host da url nao pode ser vazio")
	}

	port := parsed.Port()

	// Em dev/test, permite localhost explicitamente
	if isDevOrTest && (hostname == "localhost" || hostname == "127.0.0.1" || hostname == "::1") {
		return nil
	}

	allowedHostsEnv := os.Getenv("OUTBOUND_ALLOWED_HOSTS")

	// Bloqueia termos conhecidos de loopback ou domínios de rede interna (a menos que permitidos)
	lowerHost := strings.ToLower(hostname)
	if lowerHost == "localhost" || strings.HasSuffix(lowerHost, ".localhost") || strings.HasSuffix(lowerHost, ".local") || strings.HasSuffix(lowerHost, ".internal") {
		// Se não estiver explicitamente permitido pela allowlist
		if !isHostOrIPAllowlisted(hostname, nil, port, allowedHostsEnv) {
			return fmt.Errorf("destino proibido: host '%s' restrito para rede interna", hostname)
		}
	}

	// Resolução DNS do host para inspecionar os IPs resultantes
	ips, err := net.LookupIP(hostname)
	if err != nil {
		return fmt.Errorf("falha ao resolver host '%s' via dns: %w", hostname, err)
	}

	if len(ips) == 0 {
		return fmt.Errorf("nenhum endereco IP encontrado para o host '%s'", hostname)
	}

	for _, ip := range ips {
		if !isDevOrTest {
			if isHostOrIPAllowlisted(hostname, ip, port, allowedHostsEnv) {
				continue
			}
			if isRestrictedIP(ip) {
				return fmt.Errorf("destino proibido: endereco IP '%s' pertence a bloco restrito/privado", ip.String())
			}
		}
	}

	return nil
}

// ValidateOutboundAddress valida um endereço IP:porta resolvido no momento da conexão (net.Dialer.Control).
func ValidateOutboundAddress(network, address string, env string, allowedHosts string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("endereco de destino invalido: %s", address)
	}

	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}

	if allowedHosts == "" {
		allowedHosts = os.Getenv("OUTBOUND_ALLOWED_HOSTS")
	}

	if isHostOrIPAllowlisted("", ip, port, allowedHosts) {
		return nil
	}

	isDevOrTest := env == "development" || env == "test"
	if isDevOrTest && (ip.IsLoopback() || ip.Equal(net.IPv4(127, 0, 0, 1)) || ip.Equal(net.IPv6loopback)) {
		return nil
	}

	if isRestrictedIP(ip) {
		return fmt.Errorf("conexao bloqueada por politica anti-ssrf: ip %s restrito", ip.String())
	}

	return nil
}

// isHostOrIPAllowlisted verifica se um host ou IP/porta coincide com a allowlist OUTBOUND_ALLOWED_HOSTS
func isHostOrIPAllowlisted(host string, ip net.IP, port string, allowlistStr string) bool {
	if strings.TrimSpace(allowlistStr) == "" {
		return false
	}

	if ip != nil {
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
	}

	entries := strings.Split(allowlistStr, ",")
	for _, raw := range entries {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}

		var entryHost, entryPort string
		if strings.Contains(entry, ":") {
			h, p, err := net.SplitHostPort(entry)
			if err == nil {
				entryHost = strings.ToLower(h)
				entryPort = p
			} else {
				entryHost = strings.ToLower(entry)
			}
		} else {
			entryHost = strings.ToLower(entry)
		}

		if entryPort != "" && port != "" && entryPort != port {
			continue
		}

		// Checagem por nome de host direto
		if host != "" && strings.EqualFold(host, entryHost) {
			return true
		}

		// Checagem por IP direto na allowlist
		if entryIP := net.ParseIP(entryHost); entryIP != nil {
			if v4 := entryIP.To4(); v4 != nil {
				entryIP = v4
			}
			if ip != nil && entryIP.Equal(ip) {
				return true
			}
			continue
		}

		// Se o item da allowlist for um host e tivermos um IP para verificar, resolve o host da allowlist
		if ip != nil && entryHost != "" {
			if resolvedIPs, err := net.LookupIP(entryHost); err == nil {
				for _, rip := range resolvedIPs {
					if v4 := rip.To4(); v4 != nil {
						rip = v4
					}
					if rip.Equal(ip) {
						return true
					}
				}
			}
		}
	}

	return false
}

// isRestrictedIP verifica se o IP pertence a blocos não roteáveis publicamente, especiais ou privados
func isRestrictedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}

	// Desencapsula IPv4 mapeado em IPv6 (::ffff:127.0.0.1 -> 127.0.0.1)
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}

	// Loopback (127.0.0.0/8 ou ::1)
	if ip.IsLoopback() {
		return true
	}

	// Unspecified (0.0.0.0 ou ::)
	if ip.IsUnspecified() {
		return true
	}

	// RFC 1918 (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16) e Unique Local IPv6 (fc00::/7)
	if ip.IsPrivate() {
		return true
	}

	// Link-Local (169.254.0.0/16 ou fe80::/10)
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}

	// Bloco 169.254.0.0/16 específico de metadados AWS/GCP/DigitalOcean/OpenStack
	if linkLocalMetadata.Contains(ip) {
		return true
	}

	// Carrier-Grade NAT (100.64.0.0/10)
	if carrierGradeNAT.Contains(ip) {
		return true
	}

	// Multicast
	if ip.IsMulticast() {
		return true
	}

	// Regras adicionais para IPv4
	if v4 := ip.To4(); v4 != nil {
		// "This host on this network" (0.0.0.0/8)
		if v4[0] == 0 {
			return true
		}
		// Reserved for future use (240.0.0.0/4)
		if v4[0] >= 240 {
			return true
		}
		// Benchmark testing RFC 2544 (198.18.0.0/15)
		if benchmarkTesting.Contains(v4) {
			return true
		}
	}

	return false
}
