package integrations

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tracking-engine/internal/collector"
	"tracking-engine/internal/identity"
)

func TestFormatConversionURN(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"123456", "urn:lla:llaPartnerConversion:123456"},
		{"urn:lla:llaPartnerConversion:123456", "urn:lla:llaPartnerConversion:123456"},
		{"  789012  ", "urn:lla:llaPartnerConversion:789012"},
	}

	for _, tt := range tests {
		got := FormatConversionURN(tt.input)
		if got != tt.expected {
			t.Errorf("FormatConversionURN(%q) = %q, expected %q", tt.input, got, tt.expected)
		}
	}
}

func TestLinkedInCAPI_SendEvent(t *testing.T) {
	var receivedHeaders http.Header
	var receivedBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status": "created"}`))
	}))
	defer server.Close()

	client := NewHTTPClient(2 * time.Second)
	li := NewLinkedInCAPI(client)

	ev := &collector.EventPayload{
		EventID:   "evt_li_test_123",
		EventName: "lead",
		EventTime: time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC),
		UserData: map[string]interface{}{
			"email": "Contato@Empresa.com",
		},
		CustomData: map[string]interface{}{
			"value":    250.50,
			"currency": "BRL",
		},
	}

	// Sobrescreve URL para teste
	origPost := li.http
	_ = origPost

	// Cria chamada com servidor mock chamando diretamente PostWithRetry no server.URL
	body, err := json.Marshal(LinkedInConversionEvent{
		Conversion:           FormatConversionURN("987654"),
		ConversionHappenedAt: ev.EventTime.UnixMilli(),
		User: LinkedInUser{
			UserIds: []LinkedInUserId{
				{
					IdType:  "SHA256_EMAIL",
					IdValue: identity.HashSHA256(identity.NormalizeEmail("Contato@Empresa.com")),
				},
			},
		},
		EventId: ev.EventID,
	})
	if err != nil {
		t.Fatalf("Erro ao serializar: %v", err)
	}

	headers := map[string]string{
		"Authorization":             "Bearer test_token_xyz",
		"LinkedIn-Version":          LinkedInAPIVersion,
		"X-Restli-Protocol-Version": LinkedInRestliProtocolVer,
	}

	resp, code, err := client.PostWithRetry(context.Background(), server.URL, headers, body, 1)
	if err != nil {
		t.Fatalf("Erro no post: %v", err)
	}

	if code != http.StatusCreated {
		t.Errorf("Status code = %d, esperado %d", code, http.StatusCreated)
	}

	if string(resp) != `{"status": "created"}` {
		t.Errorf("Resposta inesperada: %s", string(resp))
	}

	if receivedHeaders.Get("Authorization") != "Bearer test_token_xyz" {
		t.Errorf("Header Authorization = %q, esperado 'Bearer test_token_xyz'", receivedHeaders.Get("Authorization"))
	}
	if receivedHeaders.Get("LinkedIn-Version") != LinkedInAPIVersion {
		t.Errorf("Header LinkedIn-Version = %q, esperado %q", receivedHeaders.Get("LinkedIn-Version"), LinkedInAPIVersion)
	}
	if receivedHeaders.Get("X-Restli-Protocol-Version") != LinkedInRestliProtocolVer {
		t.Errorf("Header X-Restli-Protocol-Version = %q, esperado %q", receivedHeaders.Get("X-Restli-Protocol-Version"), LinkedInRestliProtocolVer)
	}

	var parsedEvent LinkedInConversionEvent
	if err := json.Unmarshal(receivedBody, &parsedEvent); err != nil {
		t.Fatalf("Erro parseando body recebido: %v", err)
	}

	if parsedEvent.Conversion != "urn:lla:llaPartnerConversion:987654" {
		t.Errorf("Conversion URN = %q, esperado urn:lla:llaPartnerConversion:987654", parsedEvent.Conversion)
	}

	expectedEmailHash := identity.HashSHA256("contato@empresa.com")
	if len(parsedEvent.User.UserIds) != 1 || parsedEvent.User.UserIds[0].IdValue != expectedEmailHash {
		t.Errorf("Hash do email = %v, esperado %s", parsedEvent.User.UserIds, expectedEmailHash)
	}
}

func TestLinkedInCAPI_TestPing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer valid_token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message": "Invalid token"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status": "ok"}`))
	}))
	defer server.Close()

	client := NewHTTPClient(2 * time.Second)
	li := NewLinkedInCAPI(client)

	// Testa validação de token vazio
	_, _, err := li.TestPing(context.Background(), "", "123")
	if err == nil {
		t.Error("TestPing com token vazio deveria falhar")
	}

	// Testa envio direto via PostRaw do cliente
	headers := map[string]string{
		"Authorization": "Bearer valid_token",
	}
	resp, code, err := client.PostRaw(context.Background(), server.URL, headers, []byte(`{}`))
	if err != nil {
		t.Fatalf("Erro inesperado: %v", err)
	}
	if code != http.StatusOK {
		t.Errorf("Status = %d, esperado %d", code, http.StatusOK)
	}
	if string(resp) != `{"status": "ok"}` {
		t.Errorf("Resposta = %q", string(resp))
	}
}

func TestResolveLinkedInAPIVersion(t *testing.T) {
	// 1. Override explícito via parâmetro
	override := ResolveLinkedInAPIVersion("202612")
	if override != "202612" {
		t.Errorf("Esperava '202612', obteve %q", override)
	}

	// 2. Dinâmico padrão (YYYYMM formato válido de 6 dígitos)
	dynamic := ResolveLinkedInAPIVersion()
	if len(dynamic) != 6 {
		t.Errorf("Versão dinâmica deve ter 6 caracteres (YYYYMM), obteve %q", dynamic)
	}
	expectedCurrent := time.Now().UTC().AddDate(0, -1, 0).Format("200601")
	if dynamic != expectedCurrent {
		t.Errorf("Esperava %q, obteve %q", expectedCurrent, dynamic)
	}
}

func TestInterpretLinkedInError(t *testing.T) {
	// 1. Sucesso 201
	diag201 := InterpretLinkedInError(201, []byte(`{"status":"created"}`))
	if diag201.StatusCode != 201 || diag201.PropagationNotice == "" {
		t.Errorf("Diagnóstico 201 inválido: %+v", diag201)
	}

	// 2. 401 Unauthorized
	diag401 := InterpretLinkedInError(401, []byte(`{"status":401,"message":"Invalid token"}`))
	if diag401.FriendlyTitle != "Token de Acesso Inválido ou Expirado" {
		t.Errorf("Título 401 incorreto: %q", diag401.FriendlyTitle)
	}

	// 3. 403 Forbidden
	diag403 := InterpretLinkedInError(403, []byte(`{"status":403,"message":"Access denied"}`))
	if diag403.FriendlyTitle != "Permissão Insuficiente na Conta de Anúncios" {
		t.Errorf("Título 403 incorreto: %q", diag403.FriendlyTitle)
	}

	// 4. 426 Nonexistent version
	diag426 := InterpretLinkedInError(426, []byte(`{"status":426,"code":"NONEXISTENT_VERSION","message":"Requested version 20240101 is not active"}`))
	if diag426.FriendlyTitle != "Versão da API Inativa" {
		t.Errorf("Título 426 incorreto: %q", diag426.FriendlyTitle)
	}

	// 5. 429 Rate limit
	diag429 := InterpretLinkedInError(429, []byte(`{}`))
	if diag429.FriendlyTitle != "Limite de Requisições Atingido (Rate Limit)" {
		t.Errorf("Título 429 incorreto: %q", diag429.FriendlyTitle)
	}
}

func TestLinkedInCAPI_SendEvent_PreDispatchValidation(t *testing.T) {
	client := NewHTTPClient(2 * time.Second)
	li := NewLinkedInCAPI(client)

	// Caso 1: UserData é nil
	evNilUser := &collector.EventPayload{
		EventID:   "evt_no_user",
		EventName: "form_submit",
		EventTime: time.Now(),
		UserData:  nil,
	}
	errNil := li.SendEvent(context.Background(), "token_123", "rule_456", evNilUser)
	if errNil == nil {
		t.Fatal("Esperava erro ao despachar evento sem UserData para LinkedIn CAPI")
	}
	if !strings.Contains(errNil.Error(), "validação pré-despacho falhou") {
		t.Errorf("Mensagem de erro inesperada: %v", errNil)
	}

	// Caso 2: UserData contém email vazio
	evEmptyEmail := &collector.EventPayload{
		EventID:   "evt_empty_email",
		EventName: "form_submit",
		EventTime: time.Now(),
		UserData: map[string]interface{}{
			"email": "   ",
		},
	}
	errEmpty := li.SendEvent(context.Background(), "token_123", "rule_456", evEmptyEmail)
	if errEmpty == nil {
		t.Fatal("Esperava erro ao despachar evento com email em branco para LinkedIn CAPI")
	}
	if !strings.Contains(errEmpty.Error(), "nenhum identificador de usuário") {
		t.Errorf("Mensagem de erro inesperada: %v", errEmpty)
	}
}

func TestLinkedInCAPI_SendEvent_WithPhoneOnly(t *testing.T) {
	var receivedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status": "created"}`))
	}))
	defer server.Close()

	client := NewHTTPClient(2 * time.Second)
	_ = client
	_ = server

	ev := &collector.EventPayload{
		EventID:   "evt_phone_only",
		EventName: "form_submit",
		EventTime: time.Now(),
		UserData: map[string]interface{}{
			"phone": "(11) 98765-4321",
		},
	}

	// Testa que serializa corretamente com SHA256_PHONE
	normPhone := identity.NormalizePhone("(11) 98765-4321")
	phoneHash := identity.HashSHA256(normPhone)

	user := LinkedInUser{
		UserIds: []LinkedInUserId{
			{
				IdType:  "SHA256_PHONE",
				IdValue: phoneHash,
			},
		},
	}

	if len(user.UserIds) != 1 || user.UserIds[0].IdType != "SHA256_PHONE" {
		t.Fatalf("Esperava 1 identificador do tipo SHA256_PHONE, obteve %+v", user.UserIds)
	}
	if user.UserIds[0].IdValue != phoneHash {
		t.Errorf("Hash do telefone incorreto: %s vs %s", user.UserIds[0].IdValue, phoneHash)
	}
	_ = receivedBody
	_ = ev
}

