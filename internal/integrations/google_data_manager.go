package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"tracking-engine/internal/collector"
	"tracking-engine/internal/identity"
)

// Constantes oficiais da Google Data Manager API (v1)
const (
	DataManagerIngestEndpoint = "https://datamanager.googleapis.com/v1/events:ingest"
	DataManagerScope          = "https://www.googleapis.com/auth/datamanager"
)

// ConsentStatus define os estados de consentimento segundo a diretiva DMA do Google
type ConsentStatus string

const (
	ConsentStatusUnspecified ConsentStatus = "CONSENT_STATUS_UNSPECIFIED"
	ConsentStatusGranted     ConsentStatus = "CONSENT_GRANTED"
	ConsentStatusDenied      ConsentStatus = "CONSENT_DENIED"
)

// DMConsent representa o bloco de consentimento do Google Data Manager
type DMConsent struct {
	AdUserData        ConsentStatus `json:"adUserData,omitempty"`
	AdPersonalization ConsentStatus `json:"adPersonalization,omitempty"`
}

// AccountIdentifier especifica a conta de destino no Google Ads
type AccountIdentifier struct {
	AccountType string `json:"accountType"` // "GOOGLE_ADS"
	AccountID   string `json:"accountId"`   // 10 dígitos sem traços
}

// Destination identifica a conta do Google Ads e a ação de conversão receptora
type Destination struct {
	OperatingAccount     AccountIdentifier  `json:"operatingAccount"`
	LoginAccount         *AccountIdentifier `json:"loginAccount,omitempty"`
	ProductDestinationID string             `json:"productDestinationId"` // ID numérico da conversão
}

// AdIdentifiers armazena identificadores de clique (GCLID, GBRAID, WBRAID)
type AdIdentifiers struct {
	GCLID  string `json:"gclid,omitempty"`
	GBRAID string `json:"gbraid,omitempty"`
	WBRAID string `json:"wbraid,omitempty"`
}

// UserIdentifier representa dados de contato do usuário em formato hash SHA-256
type UserIdentifier struct {
	EmailAddress string `json:"emailAddress,omitempty"`
	PhoneNumber  string `json:"phoneNumber,omitempty"`
}

// UserData agrupa identificadores de usuário
type UserData struct {
	UserIdentifiers []UserIdentifier `json:"userIdentifiers,omitempty"`
}

// DMEvent representa um evento individual no schema do Google Data Manager
type DMEvent struct {
	EventTimestamp  string         `json:"eventTimestamp"`          // RFC3339 UTC
	TransactionID   string         `json:"transactionId,omitempty"` // submission_id / order_id
	EventSource     string         `json:"eventSource"`             // "DATA_SOURCE_TYPE_WEB"
	ConversionValue float64        `json:"conversionValue,omitempty"`
	Currency        string         `json:"currency,omitempty"`      // "BRL", "USD", etc.
	AdIdentifiers   *AdIdentifiers `json:"adIdentifiers,omitempty"`
	UserData        *UserData      `json:"userData,omitempty"`
	Consent         *DMConsent     `json:"consent,omitempty"`
}

// IngestEventsRequest representa a requisição POST para events:ingest
type IngestEventsRequest struct {
	ValidateOnly bool          `json:"validateOnly,omitempty"`
	Destinations []Destination `json:"destinations"`
	Events       []DMEvent     `json:"events"`
}

// GoogleDataManagerConfig contém as credenciais e parâmetros de destino configurados por site
type GoogleDataManagerConfig struct {
	ClientID           string `json:"client_id"`
	ClientSecret       string `json:"client_secret"`
	RefreshToken       string `json:"refresh_token"`
	OperatingAccountID string `json:"operating_account_id"` // Google Ads Customer ID (ex: "4895101364")
	LoginAccountID     string `json:"login_account_id"`     // MCC ou a própria conta
	ConversionActionID string `json:"conversion_action_id"` // ID numérico padrão da ação de conversão
}

// tokenEntry armazena o token de acesso em cache thread-safe
type tokenEntry struct {
	tokenSource oauth2.TokenSource
}

// GoogleDataManager é o cliente de integração com o Google Data Manager
type GoogleDataManager struct {
	http   *HTTPClient
	mu     sync.RWMutex
	tokens map[string]*tokenEntry // key: clientID + ":" + operatingAccountID
}

// NewGoogleDataManager instancia um novo cliente
func NewGoogleDataManager(client *HTTPClient) *GoogleDataManager {
	return &GoogleDataManager{
		http:   client,
		tokens: make(map[string]*tokenEntry),
	}
}

// getTokenSource obtém ou inicializa o TokenSource OAuth2 para a configuração fornecida
func (g *GoogleDataManager) getTokenSource(ctx context.Context, cfg GoogleDataManagerConfig) (oauth2.TokenSource, error) {
	if cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.RefreshToken == "" {
		return nil, fmt.Errorf("credenciais OAuth2 incompletas (client_id, client_secret e refresh_token sao obrigatorios)")
	}

	cacheKey := cfg.ClientID + ":" + cfg.OperatingAccountID

	g.mu.RLock()
	entry, exists := g.tokens[cacheKey]
	g.mu.RUnlock()

	if exists {
		return entry.tokenSource, nil
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	// Double check após lock
	if entry, exists = g.tokens[cacheKey]; exists {
		return entry.tokenSource, nil
	}

	oauthConfig := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       []string{DataManagerScope},
	}

	initialToken := &oauth2.Token{
		RefreshToken: cfg.RefreshToken,
	}

	ts := oauthConfig.TokenSource(ctx, initialToken)
	g.tokens[cacheKey] = &tokenEntry{tokenSource: ts}
	return ts, nil
}

// GetAccessToken retorna um access token válido, renovando automaticamente se expirado
func (g *GoogleDataManager) GetAccessToken(ctx context.Context, cfg GoogleDataManagerConfig) (string, error) {
	ts, err := g.getTokenSource(ctx, cfg)
	if err != nil {
		return "", err
	}

	tok, err := ts.Token()
	if err != nil {
		return "", fmt.Errorf("falha ao obter token OAuth2 do Google Data Manager: %w", err)
	}

	return tok.AccessToken, nil
}

// FormatEvent converte um EventPayload do nosso coletor para o modelo DMEvent
func FormatEvent(ev *collector.EventPayload) (*DMEvent, error) {
	if ev == nil {
		return nil, fmt.Errorf("evento nulo")
	}

	// 1. Identificadores de anúncio
	var adIds *AdIdentifiers
	if ev.Attribution.GCLID != "" || ev.Attribution.GBRAID != "" || ev.Attribution.WBRAID != "" {
		adIds = &AdIdentifiers{
			GCLID:  ev.Attribution.GCLID,
			GBRAID: ev.Attribution.GBRAID,
			WBRAID: ev.Attribution.WBRAID,
		}
	}

	// 2. Identificadores de usuário com hash SHA-256
	var userData *UserData
	var userIds []UserIdentifier

	if ev.UserData != nil {
		var emailHash, phoneHash string
		if em, ok := ev.UserData["email"].(string); ok && em != "" {
			emailHash = identity.HashSHA256(identity.NormalizeEmail(em))
		}
		if ph, ok := ev.UserData["phone"].(string); ok && ph != "" {
			phoneHash = identity.HashSHA256(identity.NormalizePhone(ph))
		}

		if emailHash != "" || phoneHash != "" {
			userIds = append(userIds, UserIdentifier{
				EmailAddress: emailHash,
				PhoneNumber:  phoneHash,
			})
			userData = &UserData{UserIdentifiers: userIds}
		}
	}

	// 3. Validação do Gatekeeper: deve ter ao menos um identificador de correspondência
	if adIds == nil && userData == nil {
		return nil, fmt.Errorf("evento ignorado: nenhum identificador de clique ou usuario presente")
	}

	// 4. Mapeamento de Consentimento DMA (Consent Mode v2)
	var consent *DMConsent
	if ev.Consent.Collected {
		if ev.Consent.Marketing {
			consent = &DMConsent{
				AdUserData:        ConsentStatusGranted,
				AdPersonalization: ConsentStatusGranted,
			}
		} else {
			consent = &DMConsent{
				AdUserData:        ConsentStatusDenied,
				AdPersonalization: ConsentStatusDenied,
			}
		}
	} else {
		// Não coletado: registrar explicitamente como não especificado
		consent = &DMConsent{
			AdUserData:        ConsentStatusUnspecified,
			AdPersonalization: ConsentStatusUnspecified,
		}
	}

	// 5. Transaction ID para deduplicação
	txnID := ev.SubmissionID
	if txnID == "" {
		txnID = ev.EventID
	}

	// 6. Valores de conversão
	var val float64
	currency := "BRL"
	if ev.CustomData != nil {
		if v, ok := ev.CustomData["value"].(float64); ok {
			val = v
		}
		if c, ok := ev.CustomData["currency"].(string); ok && c != "" {
			currency = c
		}
	}

	eventTime := ev.EventTime
	if eventTime.IsZero() {
		eventTime = time.Now().UTC()
	}

	return &DMEvent{
		EventTimestamp:  eventTime.UTC().Format(time.RFC3339),
		TransactionID:   txnID,
		EventSource:     "DATA_SOURCE_TYPE_WEB",
		ConversionValue: val,
		Currency:        currency,
		AdIdentifiers:   adIds,
		UserData:        userData,
		Consent:         consent,
	}, nil
}

// IngestEvents despacha uma lista de eventos para o Google Data Manager
func (g *GoogleDataManager) IngestEvents(ctx context.Context, cfg GoogleDataManagerConfig, events []DMEvent, validateOnly bool) ([]byte, int, error) {
	if cfg.OperatingAccountID == "" {
		return nil, 0, fmt.Errorf("operating_account_id e obrigatorio")
	}
	if cfg.ConversionActionID == "" {
		return nil, 0, fmt.Errorf("conversion_action_id e obrigatorio")
	}
	if len(events) == 0 {
		return nil, 0, fmt.Errorf("nenhum evento para despachar")
	}
	if len(events) > 2000 {
		return nil, 0, fmt.Errorf("tamanho do lote (%d) excede o limite maximo de 2000 eventos da API", len(events))
	}

	token, err := g.GetAccessToken(ctx, cfg)
	if err != nil {
		return nil, 0, fmt.Errorf("falha de autenticacao OAuth2: %w", err)
	}

	var loginAcc *AccountIdentifier
	if cfg.LoginAccountID != "" && cfg.LoginAccountID != cfg.OperatingAccountID {
		loginAcc = &AccountIdentifier{
			AccountType: "GOOGLE_ADS",
			AccountID:   cfg.LoginAccountID,
		}
	}

	destination := Destination{
		OperatingAccount: AccountIdentifier{
			AccountType: "GOOGLE_ADS",
			AccountID:   cfg.OperatingAccountID,
		},
		LoginAccount:         loginAcc,
		ProductDestinationID: cfg.ConversionActionID,
	}

	reqBody := IngestEventsRequest{
		ValidateOnly: validateOnly,
		Destinations: []Destination{destination},
		Events:       events,
	}

	bodyJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, 0, fmt.Errorf("erro serializando IngestEventsRequest: %w", err)
	}

	headers := map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	}

	return g.http.PostWithRetry(ctx, DataManagerIngestEndpoint, headers, bodyJSON, 3)
}

// TestPing realiza uma validação sintética contra a Google Data Manager API utilizando validateOnly: true
func (g *GoogleDataManager) TestPing(ctx context.Context, cfg GoogleDataManagerConfig) ([]byte, int, error) {
	testEvent := DMEvent{
		EventTimestamp: time.Now().UTC().Format(time.RFC3339),
		TransactionID:  fmt.Sprintf("test_ping_%d", time.Now().Unix()),
		EventSource:    "DATA_SOURCE_TYPE_WEB",
		AdIdentifiers: &AdIdentifiers{
			GCLID: "test_gclid_validation_sample_123456",
		},
		UserData: &UserData{
			UserIdentifiers: []UserIdentifier{
				{
					EmailAddress: identity.HashSHA256(identity.NormalizeEmail("teste@trackeamentohn.com.br")),
					PhoneNumber:  identity.HashSHA256(identity.NormalizePhone("+5511999999999")),
				},
			},
		},
		Consent: &DMConsent{
			AdUserData:        ConsentStatusGranted,
			AdPersonalization: ConsentStatusGranted,
		},
		ConversionValue: 1.0,
		Currency:        "BRL",
	}

	return g.IngestEvents(ctx, cfg, []DMEvent{testEvent}, true) // validateOnly = true
}
