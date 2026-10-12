package integrations

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestHTTPClient_RedirectHandling(t *testing.T) {
	// Cria servidor que redireciona 301
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status": "ok"}`))
	}))
	defer targetServer.Close()

	redirectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, targetServer.URL, http.StatusMovedPermanently)
	}))
	defer redirectServer.Close()

	client := NewHTTPClient(2 * time.Second)

	// PostRaw deve reportar o redirect como erro informativo
	_, statusCode, err := client.PostRaw(context.Background(), redirectServer.URL, nil, []byte(`{}`))
	if statusCode != http.StatusMovedPermanently {
		t.Errorf("esperava status 301, obteve %d", statusCode)
	}
	if err == nil {
		t.Fatal("esperava erro de redirecionamento bloqueado, obteve nil")
	}
	if !strings.Contains(err.Error(), "redirecionamento HTTP 301") {
		t.Errorf("mensagem de erro inesperada: %v", err)
	}

	// PostWithRetry também deve falhar sem retentar
	_, respCode, retryErr := client.PostWithRetry(context.Background(), redirectServer.URL, nil, []byte(`{}`), 2)
	if respCode != http.StatusMovedPermanently {
		t.Errorf("esperava status 301 no retry, obteve %d", respCode)
	}
	if retryErr == nil {
		t.Fatal("esperava erro de redirecionamento no PostWithRetry, obteve nil")
	}
}

func TestHTTPClient_SSRFBlockedControl(t *testing.T) {
	oldEnv := os.Getenv("ENV")
	os.Setenv("ENV", "production")
	defer os.Setenv("ENV", oldEnv)

	client := NewHTTPClient(2 * time.Second)

	// Tenta conectar em 127.0.0.1 em modo production
	_, _, err := client.PostRaw(context.Background(), "http://127.0.0.1:12345/api", nil, []byte(`{}`))
	if err == nil {
		t.Fatal("esperava erro de conexao bloqueada pelo Control em producao, obteve nil")
	}
	if !strings.Contains(err.Error(), "conexao bloqueada") && !strings.Contains(err.Error(), "restrito") {
		t.Errorf("esperava erro de bloqueio SSRF, obteve: %v", err)
	}
}
