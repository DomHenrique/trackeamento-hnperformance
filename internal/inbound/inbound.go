package inbound

import (
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

var (
	ErrNoContactIdentifier = errors.New("nenhum identificador de contato (email ou telefone) encontrado no payload")
	ErrInvalidPayload      = errors.New("payload inválido ou formato não suportado")
	ErrIgnoredEvent        = errors.New("evento ignorado (não representa conversão/fechamento)")
)

// NormalizedCRMEvent representa a estrutura canônica de uma conversão externa recebida via webhook de CRM/ERP/Gateway
type NormalizedCRMEvent struct {
	Provider        string                 `json:"provider"`                   // rdstation, hubspot, pipedrive, activecampaign, omie, asaas, kiwify, custom
	EventName       string                 `json:"event_name"`                 // purchase, deal_won, lead_qualified
	ProviderEventID string                 `json:"provider_event_id,omitempty"` // ID único do evento no provedor (se houver)
	Email           string                 `json:"email,omitempty"`            // Email do cliente
	Phone           string                 `json:"phone,omitempty"`            // Telefone/WhatsApp (E.164 ou normalizado)
	Name            string                 `json:"name,omitempty"`             // Nome do contato
	Value           float64                `json:"value"`                      // Valor financeiro legado (float64)
	ValueCents      int64                  `json:"value_cents"`                // Valor financeiro rigoroso em centavos/unidade mínima (int64)
	Currency        string                 `json:"currency"`                   // Código ISO 4217 de 3 letras (BRL, USD, EUR)
	OrderID         string                 `json:"order_id,omitempty"`         // ID único da transação/deal para idempotência
	Status          string                 `json:"status,omitempty"`           // Status na ferramenta de origem
	VisitorID       string                 `json:"visitor_id,omitempty"`       // Opcional: visitor_id capturado pelo form/CRM
	GCLID           string                 `json:"gclid,omitempty"`            // Google Click ID
	FBCLID          string                 `json:"fbclid,omitempty"`           // Facebook Click ID
	FBP             string                 `json:"fbp,omitempty"`              // Facebook Browser ID (_fbp)
	FBC             string                 `json:"fbc,omitempty"`              // Facebook Click ID (_fbc)
	TTCLID          string                 `json:"ttclid,omitempty"`           // TikTok Click ID
	OccurredAt      time.Time              `json:"occurred_at"`                // Momento em que a conversão ocorreu
	RawPayload      map[string]interface{} `json:"raw_payload,omitempty"`      // Cópia do payload recebido para auditoria
	CustomData      map[string]interface{} `json:"custom_data,omitempty"`      // Dados adicionais do negócio
}

// InboundAdapter define a interface que cada conector de CRM/ERP deve satisfazer
type InboundAdapter interface {
	Parse(c *fiber.Ctx, body []byte) (*NormalizedCRMEvent, error)
}

// GetAdapter retorna o adapter adequado para o provedor informado
func GetAdapter(provider string) InboundAdapter {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "rdstation", "rd_station", "rd-station":
		return &RDStationAdapter{}
	case "hubspot", "hub_spot":
		return &HubSpotAdapter{}
	case "pipedrive", "pipe_drive":
		return &PipedriveAdapter{}
	case "activecampaign", "active_campaign", "active-campaign":
		return &ActiveCampaignAdapter{}
	case "omie", "omie_erp":
		return &OmieAdapter{}
	case "asaas":
		return &AsaasAdapter{}
	case "kiwify":
		return &KiwifyAdapter{}
	case "custom", "n8n", "zapier", "make", "webhook":
		return &CustomAdapter{}
	default:
		return &CustomAdapter{}
	}
}
