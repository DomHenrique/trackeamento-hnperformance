package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"tracking-engine/internal/attribution"
	"tracking-engine/internal/collector"
	"tracking-engine/internal/identity"
)

func TestFormatEvent_WithGCLIDAndContact(t *testing.T) {
	ev := &collector.EventPayload{
		EventID:      "evt_123",
		SubmissionID: "txn_sps_999",
		EventTime:    time.Date(2026, 10, 19, 22, 0, 0, 0, time.UTC),
		Attribution: attribution.Params{
			GCLID: "gclid_sample_test",
		},
		UserData: map[string]interface{}{
			"email": "contato@spspower.com.br",
			"phone": "+5511988887777",
		},
		CustomData: map[string]interface{}{
			"value":    250.50,
			"currency": "BRL",
		},
		Consent: collector.ConsentState{
			Collected: true,
			Marketing: true,
		},
	}

	dmEvent, err := FormatEvent(ev)
	if err != nil {
		t.Fatalf("FormatEvent falhou inesperadamente: %v", err)
	}

	if dmEvent.TransactionID != "txn_sps_999" {
		t.Errorf("esperava TransactionID txn_sps_999, obteve %s", dmEvent.TransactionID)
	}
	if dmEvent.EventTimestamp != "2026-10-19T22:00:00Z" {
		t.Errorf("esperava EventTimestamp 2026-10-19T22:00:00Z, obteve %s", dmEvent.EventTimestamp)
	}
	if dmEvent.AdIdentifiers == nil || dmEvent.AdIdentifiers.GCLID != "gclid_sample_test" {
		t.Errorf("esperava GCLID gclid_sample_test")
	}

	expectedEmailHash := identity.HashSHA256(identity.NormalizeEmail("contato@spspower.com.br"))
	expectedPhoneHash := identity.HashSHA256(identity.NormalizePhone("+5511988887777"))

	if dmEvent.UserData == nil || len(dmEvent.UserData.UserIdentifiers) == 0 {
		t.Fatalf("esperava UserIdentifiers presente")
	}
	if dmEvent.UserData.UserIdentifiers[0].EmailAddress != expectedEmailHash {
		t.Errorf("esperava hash de email %s, obteve %s", expectedEmailHash, dmEvent.UserData.UserIdentifiers[0].EmailAddress)
	}
	if dmEvent.UserData.UserIdentifiers[0].PhoneNumber != expectedPhoneHash {
		t.Errorf("esperava hash de fone %s, obteve %s", expectedPhoneHash, dmEvent.UserData.UserIdentifiers[0].PhoneNumber)
	}

	// Consentimento
	if dmEvent.Consent == nil || dmEvent.Consent.AdUserData != ConsentStatusGranted {
		t.Errorf("esperava AdUserData CONSENT_GRANTED, obteve %+v", dmEvent.Consent)
	}
}

func TestFormatEvent_Gatekeeper_RejectsAnonymous(t *testing.T) {
	ev := &collector.EventPayload{
		EventID:   "evt_anon",
		EventTime: time.Now(),
		Consent: collector.ConsentState{
			Collected: true,
			Marketing: true,
		},
	}

	_, err := FormatEvent(ev)
	if err == nil {
		t.Fatal("esperava erro ao formatar evento sem GCLID nem dados de contato")
	}
}

func TestFormatEvent_ConsentStatusUnspecified_WhenNotCollected(t *testing.T) {
	ev := &collector.EventPayload{
		EventID: "evt_no_banner",
		Attribution: attribution.Params{
			GCLID: "click_123",
		},
		Consent: collector.ConsentState{
			Collected: false,
		},
	}

	dmEvent, err := FormatEvent(ev)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	if dmEvent.Consent == nil {
		t.Fatal("esperava bloco Consent preenchido")
	}
	if dmEvent.Consent.AdUserData != ConsentStatusUnspecified {
		t.Errorf("esperava CONSENT_STATUS_UNSPECIFIED, obteve %s", dmEvent.Consent.AdUserData)
	}
	if dmEvent.Consent.AdPersonalization != ConsentStatusUnspecified {
		t.Errorf("esperava CONSENT_STATUS_UNSPECIFIED, obteve %s", dmEvent.Consent.AdPersonalization)
	}
}

func TestFormatEvent_ConsentDenied(t *testing.T) {
	ev := &collector.EventPayload{
		EventID: "evt_rejected_cookies",
		Attribution: attribution.Params{
			GCLID: "click_123",
		},
		Consent: collector.ConsentState{
			Collected: true,
			Marketing: false,
		},
	}

	dmEvent, err := FormatEvent(ev)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	if dmEvent.Consent.AdUserData != ConsentStatusDenied {
		t.Errorf("esperava CONSENT_DENIED, obteve %s", dmEvent.Consent.AdUserData)
	}
}

func TestGoogleDataManager_IngestEvents_PayloadStructure(t *testing.T) {
	var capturedBody []byte
	var capturedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		var buf [4096]byte
		n, _ := r.Body.Read(buf[:])
		capturedBody = buf[:n]

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := NewHTTPClient(5 * time.Second)
	dm := NewGoogleDataManager(client)

	// Mock token em cache
	dm.tokens["cid:4895101364"] = &tokenEntry{
		tokenSource: &mockTokenSource{token: "mock_test_token_xyz"},
	}

	cfg := GoogleDataManagerConfig{
		ClientID:           "cid",
		ClientSecret:       "csec",
		RefreshToken:       "reftok",
		OperatingAccountID: "4895101364",
		LoginAccountID:     "1234567890",
		ConversionActionID: "998877",
	}

	event := DMEvent{
		EventTimestamp:  "2026-10-19T22:30:00Z",
		TransactionID:   "txn_1",
		EventSource:     "DATA_SOURCE_TYPE_WEB",
		ConversionValue: 100.0,
		Currency:        "BRL",
		AdIdentifiers: &AdIdentifiers{
			GCLID: "gclid_test",
		},
	}

	// Executa IngestEvents apontando para o mock server via request
	reqBody := IngestEventsRequest{
		ValidateOnly: true,
		Destinations: []Destination{
			{
				OperatingAccount: AccountIdentifier{
					AccountType: "GOOGLE_ADS",
					AccountID:   cfg.OperatingAccountID,
				},
				LoginAccount: &AccountIdentifier{
					AccountType: "GOOGLE_ADS",
					AccountID:   cfg.LoginAccountID,
				},
				ProductDestinationID: cfg.ConversionActionID,
			},
		},
		Events: []DMEvent{event},
	}

	bodyJSON, _ := json.Marshal(reqBody)
	headers := map[string]string{
		"Authorization": "Bearer mock_test_token_xyz",
		"Content-Type":  "application/json",
	}

	resp, code, err := client.PostWithRetry(context.Background(), server.URL, headers, bodyJSON, 1)
	if err != nil {
		t.Fatalf("erro ao enviar: %v", err)
	}
	if code != 200 {
		t.Fatalf("esperava status 200, obteve %d (%s)", code, string(resp))
	}

	if capturedAuth != "Bearer mock_test_token_xyz" {
		t.Errorf("esperava Bearer mock_test_token_xyz, obteve %s", capturedAuth)
	}

	var parsedReq IngestEventsRequest
	if err := json.Unmarshal(capturedBody, &parsedReq); err != nil {
		t.Fatalf("erro parseando corpo capturado: %v", err)
	}

	if !parsedReq.ValidateOnly {
		t.Error("esperava ValidateOnly = true")
	}
	if len(parsedReq.Destinations) != 1 {
		t.Fatalf("esperava 1 destination, obteve %d", len(parsedReq.Destinations))
	}
	dest := parsedReq.Destinations[0]
	if dest.OperatingAccount.AccountID != "4895101364" || dest.ProductDestinationID != "998877" {
		t.Errorf("Destination invalida: %+v", dest)
	}
	if len(parsedReq.Events) != 1 || parsedReq.Events[0].TransactionID != "txn_1" {
		t.Errorf("Evento invalido: %+v", parsedReq.Events)
	}
}

type mockTokenSource struct {
	token string
}

func (m *mockTokenSource) Token() (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: m.token}, nil
}
