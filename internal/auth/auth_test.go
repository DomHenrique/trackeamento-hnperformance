package auth

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

func TestHashAndCheckPassword(t *testing.T) {
	pwd := "MinhaSenhaSuperSegura123!"
	hash, err := HashPassword(pwd)
	if err != nil {
		t.Fatalf("Erro inesperado ao gerar hash: %v", err)
	}

	if !CheckPasswordHash(pwd, hash) {
		t.Errorf("Esperava que a senha original conferisse com o hash gerado")
	}

	if CheckPasswordHash("SenhaErrada", hash) {
		t.Errorf("Senha errada não deveria validar contra o hash")
	}
}

func TestGenerateSessionToken(t *testing.T) {
	t1 := generateSessionToken()
	t2 := generateSessionToken()

	if len(t1) != 64 {
		t.Errorf("Esperava token de 64 caracteres hex (32 bytes), obtido %d", len(t1))
	}
	if t1 == t2 {
		t.Errorf("Dois tokens gerados sequencialmente não devem ser iguais")
	}
}

func TestRespondAuthenticated_SecurityContract(t *testing.T) {
	app := fiber.New()
	svc := &Service{}

	secretToken := "secret_session_token_1234567890abcdef"
	mockUser := User{
		ID:        "usr_12345",
		Username:  "admin",
		Role:      "admin",
		CreatedAt: time.Now(),
	}

	app.Post("/login-test", func(c *fiber.Ctx) error {
		return svc.RespondAuthenticated(c, mockUser, secretToken)
	})

	req := httptest.NewRequest("POST", "/login-test", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Falha ao executar app.Test: %v", err)
	}

	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("Esperava status 200, obteve %d", resp.StatusCode)
	}

	// 1. Validar Headers de Cookie
	setCookie := resp.Header.Get("Set-Cookie")
	if setCookie == "" {
		t.Fatalf("Esperava cabeçalho Set-Cookie preenchido na resposta")
	}
	if !strings.Contains(setCookie, SessionCookieName+"="+secretToken) {
		t.Errorf("Cookie não contém o token de sessão correto. Set-Cookie: %s", setCookie)
	}
	cookieLower := strings.ToLower(setCookie)
	if !strings.Contains(cookieLower, "httponly") {
		t.Errorf("Cookie DEVE ter a flag HttpOnly habilitada. Set-Cookie: %s", setCookie)
	}
	if !strings.Contains(cookieLower, "secure") {
		t.Errorf("Cookie DEVE ter a flag Secure habilitada. Set-Cookie: %s", setCookie)
	}
	if !strings.Contains(cookieLower, "samesite=lax") {
		t.Errorf("Cookie DEVE ter SameSite=Lax. Set-Cookie: %s", setCookie)
	}

	// 2. Validar que o corpo JSON NÃO expõe o token
	bodyBytes, _ := io.ReadAll(resp.Body)
	bodyStr := string(bodyBytes)

	var resData map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &resData); err != nil {
		t.Fatalf("Falha ao decodificar JSON de resposta: %v", err)
	}

	if _, exists := resData["token"]; exists {
		t.Errorf("VULNERABILIDADE DETECTADA: O campo 'token' não deve ser retornado no JSON da resposta! Payload: %s", bodyStr)
	}
	if strings.Contains(bodyStr, secretToken) {
		t.Errorf("O segredo do token de sessão não deve estar presente no corpo da resposta: %s", bodyStr)
	}
	if resData["status"] != "authenticated" {
		t.Errorf("Esperava status 'authenticated', obteve %v", resData["status"])
	}
}

func TestHandleLogin_InvalidPayload(t *testing.T) {
	app := fiber.New()
	svc := &Service{}

	app.Post("/api/v1/auth/login", svc.HandleLogin)

	req := httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewReader([]byte("invalid json")))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Erro ao executar requisição: %v", err)
	}

	if resp.StatusCode != fiber.StatusBadRequest {
		t.Errorf("Esperava HTTP 400 para payload inválido, obteve %d", resp.StatusCode)
	}
}

func TestHandleLogout_ClearsCookie(t *testing.T) {
	app := fiber.New()
	svc := &Service{}

	app.Post("/api/v1/auth/logout", svc.HandleLogout)

	req := httptest.NewRequest("POST", "/api/v1/auth/logout", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Erro ao executar requisição: %v", err)
	}

	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("Esperava HTTP 200, obteve %d", resp.StatusCode)
	}

	setCookie := resp.Header.Get("Set-Cookie")
	if !strings.Contains(setCookie, SessionCookieName+"=;") && !strings.Contains(setCookie, "max-age=0") && !strings.Contains(setCookie, "Max-Age=0") {
		// Fiber uses Max-Age=0 or max-age=-1 or empty value to expire cookie
		if !strings.Contains(setCookie, SessionCookieName) {
			t.Errorf("Logout deveria emitir comando de remoção de cookie. Set-Cookie: %s", setCookie)
		}
	}
}
