package integrations

import (
	"net"
	"testing"
)

func TestValidateOutboundURL(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		env     string
		wantErr bool
	}{
		{
			name:    "Vazia",
			rawURL:  "",
			env:     "production",
			wantErr: true,
		},
		{
			name:    "Esquema http proibido em producao",
			rawURL:  "http://example.com/api",
			env:     "production",
			wantErr: true,
		},
		{
			name:    "Esquema ftp proibido",
			rawURL:  "ftp://example.com/api",
			env:     "production",
			wantErr: true,
		},
		{
			name:    "Loopback 127.0.0.1 bloqueado em producao",
			rawURL:  "https://127.0.0.1:8080/test",
			env:     "production",
			wantErr: true,
		},
		{
			name:    "Localhost bloqueado em producao",
			rawURL:  "https://localhost:8080/test",
			env:     "production",
			wantErr: true,
		},
		{
			name:    "Metadados de nuvem 169.254.169.254 bloqueado",
			rawURL:  "https://169.254.169.254/latest/meta-data/",
			env:     "production",
			wantErr: true,
		},
		{
			name:    "RFC1918 10.0.0.1 bloqueado em producao",
			rawURL:  "https://10.0.0.1/admin",
			env:     "production",
			wantErr: true,
		},
		{
			name:    "RFC1918 192.168.1.1 bloqueado em producao",
			rawURL:  "https://192.168.1.1/setup",
			env:     "production",
			wantErr: true,
		},
		{
			name:    "RFC1918 172.20.0.2 bloqueado em producao",
			rawURL:  "https://172.20.0.2:6379",
			env:     "production",
			wantErr: true,
		},
		{
			name:    "Localhost permitido em ambiente de teste/dev",
			rawURL:  "http://localhost:8080/test",
			env:     "development",
			wantErr: false,
		},
		{
			name:    "127.0.0.1 permitido em ambiente de teste",
			rawURL:  "http://127.0.0.1:8080/test",
			env:     "test",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateOutboundURL(tt.rawURL, tt.env)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateOutboundURL(%q, %q) erro = %v, wantErr = %v", tt.rawURL, tt.env, err, tt.wantErr)
			}
		})
	}
}

func TestIsRestrictedIP(t *testing.T) {
	// Garante que nil retorna restrito
	if !isRestrictedIP(nil) {
		t.Errorf("isRestrictedIP(nil) deveria ser true")
	}

	restrictedIPs := []string{
		"127.0.0.1",
		"10.0.0.1",
		"172.16.0.1",
		"192.168.1.1",
		"169.254.169.254",
		"100.64.0.1",
		"0.0.0.0",
		"240.0.0.1",
		"198.18.0.1",
		"::1",
		"::",
		"fc00::1",
		"fe80::1",
		"::ffff:127.0.0.1",
		"::ffff:10.0.0.1",
		"::ffff:169.254.169.254",
	}

	for _, ipStr := range restrictedIPs {
		ip := net.ParseIP(ipStr)
		if !isRestrictedIP(ip) {
			t.Errorf("isRestrictedIP(%s) deveria ser true", ipStr)
		}
	}

	publicIPs := []string{
		"8.8.8.8",
		"1.1.1.1",
		"157.240.1.35",
		"2606:4700:4700::1111",
	}

	for _, ipStr := range publicIPs {
		ip := net.ParseIP(ipStr)
		if isRestrictedIP(ip) {
			t.Errorf("isRestrictedIP(%s) deveria ser false para IP publico", ipStr)
		}
	}
}

func TestValidateOutboundAddress(t *testing.T) {
	// Produção: bloqueia IP restrito
	if err := ValidateOutboundAddress("tcp", "127.0.0.1:8080", "production", ""); err == nil {
		t.Errorf("ValidateOutboundAddress deveria bloquear 127.0.0.1 em producao")
	}

	// Desenvolvimento: permite 127.0.0.1
	if err := ValidateOutboundAddress("tcp", "127.0.0.1:8080", "development", ""); err != nil {
		t.Errorf("ValidateOutboundAddress deveria permitir 127.0.0.1 em dev: %v", err)
	}

	// Produção com allowlist: permite IP autorizado
	if err := ValidateOutboundAddress("tcp", "172.20.0.5:8080", "production", "172.20.0.5:8080"); err != nil {
		t.Errorf("ValidateOutboundAddress deveria permitir IP da allowlist: %v", err)
	}

	// Produção com allowlist diferente: bloqueia
	if err := ValidateOutboundAddress("tcp", "172.20.0.6:8080", "production", "172.20.0.5:8080"); err == nil {
		t.Errorf("ValidateOutboundAddress deveria bloquear IP que nao esta na allowlist")
	}
}
