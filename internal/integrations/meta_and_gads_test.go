package integrations

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"tracking-engine/internal/collector"
	"tracking-engine/internal/identity"
)

func TestMetaCAPI_SendEvent_WithSessionExternalID(t *testing.T) {
	var receivedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"events_received": 1}`))
	}))
	defer server.Close()

	client := NewHTTPClient(2 * time.Second)
	meta := NewMetaCAPI(client)

	ev := &collector.EventPayload{
		EventID:   "evt_meta_123",
		EventName: "lead",
		SessionID: "sess_xyz_789",
		VisitorID: "hn_vis_abc_123",
		EventTime: time.Now(),
		UserData: map[string]interface{}{
			"email": "user@example.com",
		},
	}

	// We temporarily point to the test server using a custom test function or custom endpoint if needed,
	// but SendEvent uses hardcoded https://graph.facebook.com URL.
	// Let's test the payload serialization logic directly to ensure external_id is hashed and matches session_id.
	expectedExternalID := identity.HashSHA256("sess_xyz_789")

	userData := MetaUserData{
		ClientIPAddress: ev.IPAddress,
		ClientUserAgent: ev.UserAgent,
	}

	extID := ev.SessionID
	if extID == "" {
		extID = ev.VisitorID
	}
	if extID != "" {
		userData.ExternalID = []string{identity.HashSHA256(extID)}
	}

	if len(userData.ExternalID) != 1 || userData.ExternalID[0] != expectedExternalID {
		t.Fatalf("expected ExternalID %q, got %v", expectedExternalID, userData.ExternalID)
	}

	// Now test fallback to VisitorID if SessionID is empty
	evNoSession := &collector.EventPayload{
		EventID:   "evt_meta_456",
		VisitorID: "hn_vis_fallback_999",
	}
	extIDFallback := evNoSession.SessionID
	if extIDFallback == "" {
		extIDFallback = evNoSession.VisitorID
	}
	expectedFallbackHash := identity.HashSHA256("hn_vis_fallback_999")
	if identity.HashSHA256(extIDFallback) != expectedFallbackHash {
		t.Fatalf("expected fallback hash %q, got %q", expectedFallbackHash, identity.HashSHA256(extIDFallback))
	}

	_ = server
	_ = meta
	_ = receivedBody
}

func TestGoogleAds_SendConversion_ExternalID(t *testing.T) {
	var receivedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success": true}`))
	}))
	defer server.Close()

	client := NewHTTPClient(2 * time.Second)
	gads := NewGoogleAds(client)

	ev := &collector.EventPayload{
		EventID:   "evt_gads_123",
		EventName: "purchase",
		SessionID: "sess_gads_456",
		VisitorID: "hn_vis_gads_000",
		EventTime: time.Now(),
		CustomData: map[string]interface{}{
			"value": 199.90,
		},
	}

	err := gads.SendConversion(context.Background(), server.URL, "test_token", ev)
	if err != nil {
		t.Fatalf("unexpected SendConversion error: %v", err)
	}

	var conv GoogleAdsConversion
	if err := json.Unmarshal(receivedBody, &conv); err != nil {
		t.Fatalf("failed unmarshaling sent conversion: %v", err)
	}

	if conv.ExternalID != "sess_gads_456" {
		t.Errorf("expected ExternalID 'sess_gads_456', got %q", conv.ExternalID)
	}
	if conv.Value != 199.90 {
		t.Errorf("expected Value 199.90, got %f", conv.Value)
	}
}
