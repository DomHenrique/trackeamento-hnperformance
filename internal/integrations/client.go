package integrations

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"
)

type HTTPClient struct {
	client *http.Client
}

func NewHTTPClient(timeout time.Duration) *HTTPClient {
	dialer := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(network, address string, c syscall.RawConn) error {
			env := os.Getenv("ENV")
			if env == "" {
				if flag.Lookup("test.v") != nil {
					env = "test"
				} else {
					env = "development"
				}
			}
			allowedHosts := os.Getenv("OUTBOUND_ALLOWED_HOSTS")
			return ValidateOutboundAddress(network, address, env, allowedHosts)
		},
	}

	transport := &http.Transport{
		Proxy:               nil, // Explicitamente desativa proxies para validar o IP real de destino
		DialContext:         dialer.DialContext,
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 50,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 5 * time.Second,
	}

	return &HTTPClient{
		client: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// Integrações e webhooks não devem seguir redirects automáticos
				return http.ErrUseLastResponse
			},
		},
	}
}

// PostWithRetry executa uma requisição HTTP POST com tentativas e backoff exponencial
func (h *HTTPClient) PostWithRetry(ctx context.Context, url string, headers map[string]string, body []byte, maxRetries int) ([]byte, int, error) {
	var lastErr error
	var respCode int
	backoff := 500 * time.Millisecond

	for attempt := 1; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(body))
		if err != nil {
			return nil, 0, fmt.Errorf("erro criando request: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := h.client.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(backoff)
			backoff *= 2
			continue
		}

		// Limita leitura a no máximo 2MB para proteção contra OOM/respostas abusivas
		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
		_ = resp.Body.Close()
		respCode = resp.StatusCode

		if readErr != nil {
			lastErr = readErr
			time.Sleep(backoff)
			backoff *= 2
			continue
		}

		// Se status for 2xx, sucesso imediato
		if respCode >= 200 && respCode < 300 {
			return respBody, respCode, nil
		}

		// Se status for 3xx (redirecionamento bloqueado), erro imediato e não retryable
		if respCode >= 300 && respCode < 400 {
			loc := resp.Header.Get("Location")
			if loc != "" {
				return respBody, respCode, fmt.Errorf("redirecionamento HTTP %d para '%s' nao suportado para integracoes (atualize a url configurada)", respCode, loc)
			}
			return respBody, respCode, fmt.Errorf("redirecionamento HTTP %d nao suportado para integracoes", respCode)
		}

		// Se for erro de cliente 4xx (exceto 429 Too Many Requests), não adianta tentar novamente
		if respCode >= 400 && respCode < 500 && respCode != 429 {
			return respBody, respCode, fmt.Errorf("erro cliente HTTP %d: %s", respCode, string(respBody))
		}

		// Se 429 ou 5xx, aplica backoff exponencial e tenta novamente
		lastErr = fmt.Errorf("erro servidor HTTP %d: %s", respCode, string(respBody))
		time.Sleep(backoff)
		backoff *= 2
	}

	return nil, respCode, fmt.Errorf("falha apos %d tentativas: %w", maxRetries, lastErr)
}

// PostRaw realiza uma requisição POST única (sem retry), retornando o corpo e status code exatos (ideal para testes de integração)
func (h *HTTPClient) PostRaw(ctx context.Context, url string, headers map[string]string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(body))
	if err != nil {
		return nil, 0, fmt.Errorf("erro criando request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if readErr != nil {
		return nil, resp.StatusCode, readErr
	}

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc := resp.Header.Get("Location")
		if loc != "" {
			return respBody, resp.StatusCode, fmt.Errorf("redirecionamento HTTP %d para '%s' nao suportado para integracoes (atualize a url configurada)", resp.StatusCode, loc)
		}
		return respBody, resp.StatusCode, fmt.Errorf("redirecionamento HTTP %d nao suportado para integracoes", resp.StatusCode)
	}

	return respBody, resp.StatusCode, nil
}

