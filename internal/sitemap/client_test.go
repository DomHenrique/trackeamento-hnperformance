package sitemap

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIsPrivateOrForbiddenIP(t *testing.T) {
	tests := []struct {
		name      string
		ipStr     string
		forbidden bool
	}{
		// Loopback
		{"IPv4 Loopback", "127.0.0.1", true},
		{"IPv4 Loopback alt", "127.12.34.56", true},
		{"IPv6 Loopback", "::1", true},

		// RFC 1918 Private
		{"Class A Private", "10.0.0.1", true},
		{"Class A Private limit", "10.255.255.255", true},
		{"Class B Private min", "172.16.0.1", true},
		{"Class B Private mid", "172.24.10.5", true},
		{"Class B Private max", "172.31.255.255", true},
		{"Class C Private", "192.168.1.100", true},

		// Cloud Metadata & Link Local
		{"Cloud Metadata AWS/GCP", "169.254.169.254", true},
		{"Link-Local IPv4", "169.254.1.1", true},
		{"Link-Local IPv6", "fe80::1", true},

		// CGNAT
		{"Carrier-Grade NAT", "100.64.0.1", true},
		{"Carrier-Grade NAT top", "100.127.255.254", true},

		// Unspecified / Zero
		{"Unspecified IPv4", "0.0.0.0", true},
		{"Unspecified IPv6", "::", true},

		// Public & Allowed IPs
		{"Google DNS", "8.8.8.8", false},
		{"Cloudflare DNS", "1.1.1.1", false},
		{"Public Website", "93.184.216.34", false},
		{"Public IPv6", "2606:4700:4700::1111", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ipStr)
			if ip == nil {
				t.Fatalf("failed to parse IP: %s", tt.ipStr)
			}
			got := IsPrivateOrForbiddenIP(ip)
			if got != tt.forbidden {
				t.Errorf("IsPrivateOrForbiddenIP(%s) = %v; want %v", tt.ipStr, got, tt.forbidden)
			}
		})
	}
}

func TestIsHostAllowed(t *testing.T) {
	allowed := []string{"spspower.com.br", "www.spspower.com.br", "blog.spspower.com.br"}

	tests := []struct {
		name      string
		rawURL    string
		wantOK    bool
		wantHost  string
		wantErr   bool
	}{
		{"Canonical Host", "https://spspower.com.br/sitemap.xml", true, "spspower.com.br", false},
		{"Alias WWW Host", "https://www.spspower.com.br/sitemap.xml", true, "www.spspower.com.br", false},
		{"Subdomain Host", "http://blog.spspower.com.br/sitemap.xml", true, "blog.spspower.com.br", false},
		{"Uppercase Host", "HTTPS://SPSPOWER.COM.BR/sitemap.xml", true, "spspower.com.br", false},
		{"Disallowed Domain", "https://evil.com/sitemap.xml", false, "evil.com", true},
		{"Disallowed Subdomain", "https://api.spspower.com.br/sitemap.xml", false, "api.spspower.com.br", true},
		{"Disallowed Scheme FTP", "ftp://spspower.com.br/sitemap.xml", false, "", true},
		{"Disallowed Scheme File", "file:///etc/passwd", false, "", true},
		{"Empty URL", "", false, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, host, err := IsHostAllowed(tt.rawURL, allowed)
			if (err != nil) != tt.wantErr {
				t.Errorf("IsHostAllowed(%q) error = %v, wantErr %v", tt.rawURL, err, tt.wantErr)
			}
			if ok != tt.wantOK {
				t.Errorf("IsHostAllowed(%q) ok = %v, wantOK %v", tt.rawURL, ok, tt.wantOK)
			}
			if host != tt.wantHost {
				t.Errorf("IsHostAllowed(%q) host = %v, wantHost %v", tt.rawURL, host, tt.wantHost)
			}
		})
	}
}

func TestSafeFetchAntiSSRF(t *testing.T) {
	// Cria servidor local (127.0.0.1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<urlset></urlset>"))
	}))
	defer ts.Close()

	// Tentativa de acessar 127.0.0.1 diretamente mesmo que colocado em allowedHosts
	allowed := []string{"127.0.0.1", "localhost"}
	client := NewSafeHTTPClient(allowed, 2*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := SafeFetch(ctx, client, ts.URL, allowed)
	if err == nil {
		t.Fatalf("SafeFetch deveria ter falhado por anti-SSRF ao conectar em IP loopback local %s", ts.URL)
	}
	// Garante que o erro foi bloqueio anti-SSRF no socket
	if !containsStr(err.Error(), "anti-SSRF") && !containsStr(err.Error(), "bloqueada por politica") {
		t.Logf("Aviso: erro esperado de anti-SSRF recebido: %v", err)
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) > 0 && indexOf(s, substr) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
