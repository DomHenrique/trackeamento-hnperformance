package integrations

import (
	"errors"
	"fmt"
	"net"
	"net/url"
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
)

// ValidateOutboundURL valida URLs contra ataques de SSRF (Server-Side Request Forgery)
// rejeitando esquemas não seguros, loopback, RFC1918, link-local e metadados de nuvem.
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

	// Em dev/test, permite localhost explicitamente
	if isDevOrTest && (hostname == "localhost" || hostname == "127.0.0.1" || hostname == "::1") {
		return nil
	}

	// Bloqueia termos conhecidos de loopback ou domínios de rede interna
	lowerHost := strings.ToLower(hostname)
	if lowerHost == "localhost" || strings.HasSuffix(lowerHost, ".localhost") || strings.HasSuffix(lowerHost, ".local") || strings.HasSuffix(lowerHost, ".internal") {
		return fmt.Errorf("destino proibido: host '%s' restrito para rede interna", hostname)
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
			if isRestrictedIP(ip) {
				return fmt.Errorf("destino proibido: endereco IP '%s' pertence a bloco restrito/privado", ip.String())
			}
		}
	}

	return nil
}

// isRestrictedIP verifica se o IP pertence a blocos não roteáveis publicamente
func isRestrictedIP(ip net.IP) bool {
	if ip == nil {
		return true
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

	return false
}
