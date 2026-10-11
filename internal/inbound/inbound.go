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
	Provider    string                 `json:"provider"`              // rdstation, hubspot, pipedrive, activecampaign, omie, asaas, kiwify, custom
	EventName   string                 `json:"event_name"`            // purchase, deal_won, lead_qualified
	Email       string                 `json:"email,omitempty"`       // Email do cliente
	Phone       string                 `json:"phone,omitempty"`       // Telefone/WhatsApp (E.164 ou normalizado)
	Name        string                 `json:"name,omitempty"`        // Nome do contato
	Value       float64                `json:"value"`                 // Valor financeiro do negócio/venda
	Currency    string                 `json:"currency"`              // BRL, USD, etc. (Default: BRL)
	OrderID     string                 `json:"order_id,omitempty"`    // ID único da transação/deal para idempotência
	Status      string                 `json:"status,omitempty"`      // Status na ferramenta de origem
	VisitorID   string                 `json:"visitor_id,omitempty"`  // Opcional: visitor_id capturado pelo form/CRM
	OccurredAt  time.Time              `json:"occurred_at"`           // Momento em que a conversão ocorreu
	RawPayload  map[string]interface{} `json:"raw_payload,omitempty"` // Cópia do payload recebido para auditoria
	CustomData  map[string]interface{} `json:"custom_data,omitempty"` // Dados adicionais do negócio
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
