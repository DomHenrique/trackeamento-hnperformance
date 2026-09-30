package integrations

import (
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
}
