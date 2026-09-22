package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"tracking-engine/internal/collector"
	"tracking-engine/internal/identity"
)

type MetaCAPI struct {
	http *HTTPClient
}

func NewMetaCAPI(client *HTTPClient) *MetaCAPI {
	return &MetaCAPI{http: client}
}

type MetaEvent struct {
	EventName      string                 `json:"event_name"`
	EventTime      int64                  `json:"event_time"`
	EventID        string                 `json:"event_id"`
	EventSourceURL string                 `json:"event_source_url"`
	ActionSource   string                 `json:"action_source"`
	UserData       MetaUserData           `json:"user_data"`
	CustomData     map[string]interface{} `json:"custom_data,omitempty"`
}

type MetaUserData struct {
	Email           []string `json:"em,omitempty"`
	Phone           []string `json:"ph,omitempty"`
	ClientIPAddress string   `json:"client_ip_address,omitempty"`
	ClientUserAgent string   `json:"client_user_agent,omitempty"`
	FBP             string   `json:"fbp,omitempty"`
	FBC             string   `json:"fbc,omitempty"`
}

type MetaPayload struct {
	Data          []MetaEvent `json:"data"`
	TestEventCode string      `json:"test_event_code,omitempty"`
}

// SendEvent formata e envia evento para a Meta Conversions API
func (m *MetaCAPI) SendEvent(ctx context.Context, pixelID, accessToken, testCode string, ev *collector.EventPayload) error {
	if pixelID == "" || accessToken == "" {
		return fmt.Errorf("pixel_id ou access_token ausentes para Meta CAPI")
	}

	userData := MetaUserData{
		ClientIPAddress: ev.IPAddress,
		ClientUserAgent: ev.UserAgent,
	}

	if ev.UserData != nil {
		if em, ok := ev.UserData["email"].(string); ok && em != "" {
			norm := identity.NormalizeEmail(em)
			userData.Email = []string{identity.HashSHA256(norm)}
		}
		if ph, ok := ev.UserData["phone"].(string); ok && ph != "" {
			norm := identity.NormalizePhone(ph)
			userData.Phone = []string{identity.HashSHA256(norm)}
		}
		if fbp, ok := ev.UserData["fbp"].(string); ok {
			userData.FBP = fbp
		}
		if fbc, ok := ev.UserData["fbc"].(string); ok {
			userData.FBC = fbc
		}
	}

	// Se fbclid estiver presente na atribuição e fbc não foi definido, monta fbc padrão
	if userData.FBC == "" && ev.Attribution.FBCLID != "" {
		userData.FBC = fmt.Sprintf("fb.1.%d.%s", time.Now().Unix(), ev.Attribution.FBCLID)
	}

	// Mapeia nome do evento para o padrão Meta
	metaEventName := mapMetaEventName(ev.EventName)

	metaEv := MetaEvent{
		EventName:      metaEventName,
		EventTime:      ev.EventTime.Unix(),
		EventID:        ev.EventID,
		EventSourceURL: ev.Attribution.PageURL,
		ActionSource:   "website",
		UserData:       userData,
		CustomData:     ev.CustomData,
	}

	payload := MetaPayload{
		Data:          []MetaEvent{metaEv},
		TestEventCode: testCode,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("erro serializando Meta payload: %w", err)
	}

	url := fmt.Sprintf("https://graph.facebook.com/v19.0/%s/events?access_token=%s", pixelID, accessToken)
	_, _, err = m.http.PostWithRetry(ctx, url, nil, body, 3)
	return err
}

// TestPing realiza um envio sintético para a Meta CAPI com retorno detalhado da resposta da Meta
func (m *MetaCAPI) TestPing(ctx context.Context, pixelID, accessToken, testCode string) ([]byte, int, error) {
	if pixelID == "" || accessToken == "" {
		return nil, 0, fmt.Errorf("pixel_id e access_token são obrigatórios")
	}

	testEvent := MetaEvent{
		EventName:      "PageView",
		EventTime:      time.Now().Unix(),
		EventID:        fmt.Sprintf("test_meta_%d", time.Now().UnixNano()),
		EventSourceURL: "https://trackeamento.hnperformancedigital.com.br/test",
		ActionSource:   "website",
		UserData: MetaUserData{
			ClientIPAddress: "127.0.0.1",
			ClientUserAgent: "Mozilla/5.0 (TrackeamentoHN-TestAgent/1.0)",
		},
		CustomData: map[string]interface{}{
			"test_mode": true,
			"source":    "TrackeamentoHN Wizard",
		},
	}

	payload := MetaPayload{
		Data:          []MetaEvent{testEvent},
		TestEventCode: testCode,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, fmt.Errorf("erro serializando Meta payload de teste: %w", err)
	}

	url := fmt.Sprintf("https://graph.facebook.com/v19.0/%s/events?access_token=%s", pixelID, accessToken)
	return m.http.PostRaw(ctx, url, nil, body)
}


func mapMetaEventName(name string) string {
	switch strings.ToLower(name) {
	case "lead":
		return "Lead"
	case "purchase":
		return "Purchase"
	case "whatsapp_click":
		return "Contact"
	case "contact":
		return "Contact"
	case "view_content", "page_view":
		return "PageView"
	case "initiate_checkout":
		return "InitiateCheckout"
	default:
		return name
	}
}
