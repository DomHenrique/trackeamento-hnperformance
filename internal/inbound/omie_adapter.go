package inbound

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

type OmieAdapter struct{}

func (a *OmieAdapter) Parse(c *fiber.Ctx, body []byte) (*NormalizedCRMEvent, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}

	norm := &NormalizedCRMEvent{
		Provider:   "omie",
		EventName:  "purchase",
		Currency:   "BRL",
		OccurredAt: time.Now(),
		RawPayload: raw,
		CustomData: make(map[string]interface{}),
	}

	// Topic Omie (ex: VendaProduto.EtapaAlterada, Financas.ContaReceber.Baixada)
	if topic, ok := raw["topic"].(string); ok {
		norm.CustomData["topic"] = topic
		norm.Status = topic
	}

	// O objeto "event" concentra os dados do webhook no Omie
	target := raw
	if ev, ok := raw["event"].(map[string]interface{}); ok {
		target = ev
	}

	// Pedido / Transação
	if idPedido, ok := target["idPedido"]; ok {
		norm.OrderID = fmt.Sprintf("%v", idPedido)
	} else if numPedido, ok := target["numeroPedido"].(string); ok && numPedido != "" {
		norm.OrderID = numPedido
	} else if nCodPed, ok := target["nCodPed"]; ok {
		norm.OrderID = fmt.Sprintf("%v", nCodPed)
	} else if idConta, ok := target["idContaReceber"]; ok {
		norm.OrderID = fmt.Sprintf("%v", idConta)
	}

	// Valor
	if v, ok := target["valorTotal"]; ok {
		norm.Value = toFloat64(v)
	} else if v, ok := target["nValorTotal"]; ok {
		norm.Value = toFloat64(v)
	} else if v, ok := target["nValorTitulo"]; ok {
		norm.Value = toFloat64(v)
	} else if v, ok := target["valorLiquido"]; ok {
		norm.Value = toFloat64(v)
	}

	// Cliente Omie
	if cliente, ok := target["cliente"].(map[string]interface{}); ok {
		if email, ok := cliente["email"].(string); ok {
			norm.Email = strings.TrimSpace(email)
		}
		if tel, ok := cliente["telefone"].(string); ok {
			norm.Phone = strings.TrimSpace(tel)
		} else if cel, ok := cliente["celular"].(string); ok {
			norm.Phone = strings.TrimSpace(cel)
		}
		if razao, ok := cliente["razaoSocial"].(string); ok {
			norm.Name = strings.TrimSpace(razao)
		} else if nome, ok := cliente["nome"].(string); ok {
			norm.Name = strings.TrimSpace(nome)
		}
	}

	// Fallback para campos diretos Omie (cEmail, cTelefone, etc.)
	if norm.Email == "" {
		if e, ok := target["cEmail"].(string); ok {
			norm.Email = strings.TrimSpace(e)
		} else if e, ok := target["email"].(string); ok {
			norm.Email = strings.TrimSpace(e)
		}
	}
	if norm.Phone == "" {
		if p, ok := target["cTelefone"].(string); ok {
			norm.Phone = strings.TrimSpace(p)
		} else if p, ok := target["cCelular"].(string); ok {
			norm.Phone = strings.TrimSpace(p)
		} else if p, ok := target["telefone"].(string); ok {
			norm.Phone = strings.TrimSpace(p)
		}
	}
	if norm.Name == "" {
		if n, ok := target["cRazaoSocial"].(string); ok {
			norm.Name = strings.TrimSpace(n)
		}
	}

	if norm.OrderID == "" {
		norm.OrderID = fmt.Sprintf("omie_%d", time.Now().UnixNano())
	}

	if norm.Email == "" && norm.Phone == "" {
		return nil, ErrNoContactIdentifier
	}

	return norm, nil
}
