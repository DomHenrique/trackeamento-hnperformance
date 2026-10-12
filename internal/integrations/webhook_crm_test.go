package integrations

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"tracking-engine/internal/attribution"
	"tracking-engine/internal/collector"
)

func TestCRMWebhook_SendWebhook_Success(t *testing.T) {
	var receivedHeaders http.Header
	var receivedBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		var err error
		receivedBody, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	client := NewHTTPClient(5 * time.Second)
	crm := NewCRMWebhook(client)

	secret := "super-secret-hmac-key"
	eventTime := time.Date(2026, 10, 11, 20, 0, 0, 0, time.UTC)

	ev := &collector.EventPayload{
		EventID:   "evt_lead_12345",
		EventName: "form_submit",
		VisitorID: "vis_999",
		SessionID: "ses_888",
		EventTime: eventTime,
		IPAddress: "203.0.113.195",
		UserAgent: "Mozilla/5.0 TestBrowser",
		DeviceType: "desktop",
		Attribution: attribution.Params{
			PageURL:     "https://example.com/checkout?utm_source=google",
			LandingPage: "https://example.com/checkout",
			Referrer:    "https://google.com",
			UTMSource:   "google",
			UTMMedium:   "cpc",
			UTMCampaign: "spring_sale",
			UTMTerm:     "best_crm",
			UTMContent:  "banner_1",
			GCLID:       "gclid_abc_123",
			GBRAID:      "gbraid_xyz",
			WBRAID:      "wbraid_123",
			FBCLID:      "fbclid_777",
			TTCLID:      "ttclid_888",
		},
		UserData: map[string]interface{}{
			"email":          "John.Doe@Example.com",
			"phone":          float64(5511999998888), // Simula número desempacotado de JSON ERP
			"name":           "John Doe",
			"company":        "Acme Corp",
			"fbp":            "fb.1.1234567.890",
			"fbc":            "fb.1.1234567.fbclid_777",
			"internal_token": "leak_me_not", // Chave arbitrária que deve ser descartada pela whitelist
			"admin_flag":     true,
		},
		CustomData: map[string]interface{}{
			"value": 1500.0,
		},
	}

	firstTouch := map[string]interface{}{
		"landing_page": "https://example.com/home",
		"utm_source":   "facebook",
		"utm_campaign": "brand_awareness",
		"gclid":        "gclid_first",
	}

	err := crm.SendWebhook(context.Background(), server.URL, secret, ev, firstTouch)
	if err != nil {
		t.Fatalf("SendWebhook falhou: %v", err)
	}

	// 1. Valida cabeçalhos de autenticação e idempotência
	if receivedHeaders.Get("Idempotency-Key") != "evt_lead_12345" {
		t.Errorf("Idempotency-Key incorreto. Esperado evt_lead_12345, obtido %s", receivedHeaders.Get("Idempotency-Key"))
	}

	if receivedHeaders.Get("Authorization") != "Bearer "+secret {
		t.Errorf("Authorization Bearer incorreto: %s", receivedHeaders.Get("Authorization"))
	}

	// X-Webhook-Secret não deve ser enviado para evitar duplicação do segredo
	if receivedHeaders.Get("X-Webhook-Secret") != "" {
		t.Errorf("X-Webhook-Secret não deveria estar presente: %s", receivedHeaders.Get("X-Webhook-Secret"))
	}

	tsStr := receivedHeaders.Get("X-HN-Timestamp")
	if tsStr == "" {
		t.Fatal("X-HN-Timestamp não foi enviado")
	}
	tsVal, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil || time.Since(time.Unix(tsVal, 0)) > 10*time.Second {
		t.Errorf("X-HN-Timestamp inválido ou antigo: %s", tsStr)
	}

	sig := receivedHeaders.Get("X-HN-Signature")
	if sig == "" {
		t.Fatal("X-HN-Signature não foi enviado")
	}

	// Validação de assinatura HMAC-SHA256
	expectedSig := ComputeWebhookSignature(secret, tsStr, receivedBody)
	if sig != expectedSig {
		t.Errorf("Assinatura HMAC inválida. Esperada %s, obtida %s", expectedSig, sig)
	}

	// 2. Valida parsing do corpo serializado
	var payload WebhookPayload
	if err := json.Unmarshal(receivedBody, &payload); err != nil {
		t.Fatalf("Erro ao deserializar corpo recebido: %v", err)
	}

	if payload.SchemaVersion != 1 {
		t.Errorf("schema_version incorreto. Esperado 1, obtido %d", payload.SchemaVersion)
	}

	if payload.EventID != "evt_lead_12345" || payload.EventName != "form_submit" {
		t.Errorf("Campos de evento incorretos: %+v", payload)
	}

	// 3. Valida normalização de contato e whitelist
	contact := payload.Contact
	if contact["email_normalized"] != "john.doe@example.com" {
		t.Errorf("email_normalized incorreto: %v", contact["email_normalized"])
	}

	if contact["phone_normalized"] != "5511999998888" {
		t.Errorf("phone_normalized incorreto (conversão float64 falhou): %v", contact["phone_normalized"])
	}

	if contact["company"] != "Acme Corp" || contact["name"] != "John Doe" {
		t.Errorf("Campos de contato permitidos ausentes: %+v", contact)
	}

	if _, exists := contact["internal_token"]; exists {
		t.Errorf("Whitelist falhou: campo sensível 'internal_token' vazou para contact")
	}
	if _, exists := contact["admin_flag"]; exists {
		t.Errorf("Whitelist falhou: campo sensível 'admin_flag' vazou para contact")
	}

	// 4. Valida conversion_touch com todos os parâmetros de atribuição
	conv := payload.ConversionTouch
	if conv["gbraid"] != "gbraid_xyz" || conv["wbraid"] != "wbraid_123" || conv["ttclid"] != "ttclid_888" {
		t.Errorf("Identificadores de anúncio ausentes em conversion_touch: %+v", conv)
	}
	if conv["utm_term"] != "best_crm" || conv["utm_content"] != "banner_1" {
		t.Errorf("Parâmetros UTM ausentes em conversion_touch: %+v", conv)
	}
	if conv["fbp"] != "fb.1.1234567.890" || conv["fbc"] != "fb.1.1234567.fbclid_777" {
		t.Errorf("fbp/fbc incorretos em conversion_touch: %+v", conv)
	}

	// 5. Valida first_touch
	if payload.FirstTouch["landing_page"] != "https://example.com/home" {
		t.Errorf("first_touch incorreto: %+v", payload.FirstTouch)
	}
}

func TestCRMWebhook_SendWebhook_NullFirstTouch(t *testing.T) {
	var receivedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		receivedBody, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewHTTPClient(5 * time.Second)
	crm := NewCRMWebhook(client)

	ev := &collector.EventPayload{
		EventID:   "evt_direct_1",
		EventName: "contact_us",
		EventTime: time.Now().UTC(),
	}

	// Sem firstTouch anterior (primeira visita anônima direta)
	err := crm.SendWebhook(context.Background(), server.URL, "", ev, nil)
	if err != nil {
		t.Fatalf("SendWebhook falhou: %v", err)
	}

	var rawMap map[string]interface{}
	if err := json.Unmarshal(receivedBody, &rawMap); err != nil {
		t.Fatalf("Erro ao decodificar JSON: %v", err)
	}

	if rawMap["first_touch"] != nil {
		t.Errorf("first_touch deveria ser null quando vazio/nil, obtido: %v", rawMap["first_touch"])
	}
}

func TestExtractStringValue(t *testing.T) {
	tests := []struct {
		input    interface{}
		expected string
	}{
		{"11999998888", "11999998888"},
		{float64(5511999998888), "5511999998888"},
		{int64(5511999998888), "5511999998888"},
		{int(12345), "12345"},
		{json.Number("5511999998888"), "5511999998888"},
		{nil, ""},
	}

	for _, tt := range tests {
		got := extractStringValue(tt.input)
		if got != tt.expected {
			t.Errorf("extractStringValue(%v) = %s, esperado %s", tt.input, got, tt.expected)
		}
	}
}

func TestReceptorSignatureVerification(t *testing.T) {
	secret := "secret-key"
	ts := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	body := []byte(`{"event_id":"evt_test"}`)

	sig := ComputeWebhookSignature(secret, ts, body)

	// Simula verificação do lado do receptor
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + string(body)))
	expected := hex.EncodeToString(mac.Sum(nil))

	if sig != expected {
		t.Errorf("Assinatura não confere com o cálculo esperado")
	}

	// Testa proteção contra adulteração de corpo
	tamperedBody := []byte(`{"event_id":"evt_hacked"}`)
	macTampered := hmac.New(sha256.New, []byte(secret))
	macTampered.Write([]byte(ts + "." + string(tamperedBody)))
	if sig == hex.EncodeToString(macTampered.Sum(nil)) {
		t.Errorf("Assinatura deveria ter falhado para corpo adulterado")
	}
}
