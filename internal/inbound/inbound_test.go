package inbound_test

import (
	"testing"
	"tracking-engine/internal/inbound"
)

func TestRDStationAdapter(t *testing.T) {
	adapter := &inbound.RDStationAdapter{}

	payload := []byte(`{
		"leads": [{
			"id": "12345",
			"email": "contato@empresa.com",
			"name": "Maria Santos",
			"mobile_phone": "11988887777",
			"opportunity": "true",
			"value": 1500.50,
			"custom_fields": {
				"visitor_id": "hn_vis_123456789"
			}
		}]
	}`)

	ev, err := adapter.Parse(nil, payload)
	if err != nil {
		t.Fatalf("esperava sucesso no parse RD Station: %v", err)
	}

	if ev.Email != "contato@empresa.com" {
		t.Errorf("esperava email contato@empresa.com, obteve %s", ev.Email)
	}
	if ev.Value != 1500.50 {
		t.Errorf("esperava valor 1500.50, obteve %v", ev.Value)
	}
	if ev.VisitorID != "hn_vis_123456789" {
		t.Errorf("esperava visitor_id hn_vis_123456789, obteve %s", ev.VisitorID)
	}
}

func TestHubSpotAdapter(t *testing.T) {
	adapter := &inbound.HubSpotAdapter{}

	payload := []byte(`{
		"dealId": "98765",
		"dealstage": "closedwon",
		"amount": 3200,
		"email": "carlos@hubspot.test",
		"phone": "21999998888"
	}`)

	ev, err := adapter.Parse(nil, payload)
	if err != nil {
		t.Fatalf("esperava sucesso no parse HubSpot: %v", err)
	}

	if ev.Email != "carlos@hubspot.test" {
		t.Errorf("esperava email carlos@hubspot.test, obteve %s", ev.Email)
	}
	if ev.Value != 3200 {
		t.Errorf("esperava valor 3200, obteve %v", ev.Value)
	}
}

func TestPipedriveAdapter(t *testing.T) {
	adapter := &inbound.PipedriveAdapter{}

	payload := []byte(`{
		"event": "updated.deal",
		"current": {
			"id": 4321,
			"title": "Venda de Consultoria",
			"value": 5000.0,
			"currency": "BRL",
			"status": "won",
			"person_id": {
				"name": "Ana Paula",
				"email": [{"value": "ana@pipedrive.test", "primary": true}],
				"phone": [{"value": "31988881234", "primary": true}]
			}
		}
	}`)

	ev, err := adapter.Parse(nil, payload)
	if err != nil {
		t.Fatalf("esperava sucesso no parse Pipedrive: %v", err)
	}

	if ev.Email != "ana@pipedrive.test" {
		t.Errorf("esperava email ana@pipedrive.test, obteve %s", ev.Email)
	}
	if ev.EventName != "deal_won" {
		t.Errorf("esperava evento deal_won, obteve %s", ev.EventName)
	}
	if ev.Value != 5000.0 {
		t.Errorf("esperava valor 5000, obteve %v", ev.Value)
	}
}

func TestOmieAdapter(t *testing.T) {
	adapter := &inbound.OmieAdapter{}

	payload := []byte(`{
		"topic": "VendaProduto.EtapaAlterada",
		"event": {
			"idPedido": 78910,
			"numeroPedido": "PV-999",
			"valorTotal": 850.00,
			"cliente": {
				"razaoSocial": "Distribuidora Beta",
				"email": "financeiro@beta.com.br",
				"telefone": "1133334444"
			}
		}
	}`)

	ev, err := adapter.Parse(nil, payload)
	if err != nil {
		t.Fatalf("esperava sucesso no parse Omie: %v", err)
	}

	if ev.Email != "financeiro@beta.com.br" {
		t.Errorf("esperava email financeiro@beta.com.br, obteve %s", ev.Email)
	}
	if ev.Value != 850.00 {
		t.Errorf("esperava valor 850, obteve %v", ev.Value)
	}
}

func TestAsaasAdapter(t *testing.T) {
	adapter := &inbound.AsaasAdapter{}

	payload := []byte(`{
		"event": "PAYMENT_RECEIVED",
		"payment": {
			"id": "pay_5544332211",
			"value": 297.00,
			"billingType": "PIX",
			"customerEmail": "pagador@asaas.test",
			"customerPhone": "11977778888",
			"externalReference": "hn_vis_999888777"
		}
	}`)

	ev, err := adapter.Parse(nil, payload)
	if err != nil {
		t.Fatalf("esperava sucesso no parse Asaas: %v", err)
	}

	if ev.Email != "pagador@asaas.test" {
		t.Errorf("esperava email pagador@asaas.test, obteve %s", ev.Email)
	}
	if ev.VisitorID != "hn_vis_999888777" {
		t.Errorf("esperava visitor_id hn_vis_999888777, obteve %s", ev.VisitorID)
	}
	if ev.Value != 297.00 {
		t.Errorf("esperava valor 297, obteve %v", ev.Value)
	}
}

func TestCustomAdapterAutoMapping(t *testing.T) {
	adapter := &inbound.CustomAdapter{}

	// Payload aninhado arbitrário vindo de n8n ou webhook customizado
	payload := []byte(`{
		"lead_info": {
			"dados_contato": {
				"email_principal": "lead.arbitrario@empresa.com",
				"whatsapp": "11999998888"
			},
			"nome_completo": "Roberto Silva"
		},
		"negocio": {
			"financeiro": {
				"total_pago": "1.250,50",
				"moeda": "BRL"
			},
			"identificadores": {
				"pedido_id": "PED-887766"
			},
			"etapa": "fechado_ganho"
		}
	}`)

	ev, err := adapter.Parse(nil, payload)
	if err != nil {
		t.Fatalf("esperava sucesso no smart auto-mapping: %v", err)
	}

	if ev.Email != "lead.arbitrario@empresa.com" {
		t.Errorf("esperava email lead.arbitrario@empresa.com, obteve %s", ev.Email)
	}
	if ev.Phone != "11999998888" {
		t.Errorf("esperava phone 11999998888, obteve %s", ev.Phone)
	}
	if ev.Value != 1250.50 {
		t.Errorf("esperava valor 1250.50, obteve %v", ev.Value)
	}
	if ev.OrderID != "PED-887766" {
		t.Errorf("esperava order_id PED-887766, obteve %s", ev.OrderID)
	}
	if ev.EventName != "deal_won" {
		t.Errorf("esperava event_name deal_won, obteve %s", ev.EventName)
	}
}

func TestActiveCampaignAdapter_URLEncoded(t *testing.T) {
	adapter := &inbound.ActiveCampaignAdapter{}

	payload := []byte(`contact%5Bemail%5D=contato%40ac.test&contact%5Bfirst_name%5D=Lucas&contact%5Blast_name%5D=Almeida&contact%5Bphone%5D=11987654321&deal%5Bid%5D=deal_7788&deal%5Bvalue%5D=1990.00&deal%5Bcurrency%5D=BRL&deal%5Bstatus%5D=won`)

	ev, err := adapter.Parse(nil, payload)
	if err != nil {
		t.Fatalf("esperava sucesso no parse ActiveCampaign urlencoded: %v", err)
	}

	if ev.Email != "contato@ac.test" {
		t.Errorf("esperava email contato@ac.test, obteve %s", ev.Email)
	}
	if ev.Name != "Lucas Almeida" {
		t.Errorf("esperava nome Lucas Almeida, obteve %s", ev.Name)
	}
	if ev.Phone != "11987654321" {
		t.Errorf("esperava telefone 11987654321, obteve %s", ev.Phone)
	}
	if ev.OrderID != "deal_7788" {
		t.Errorf("esperava order_id deal_7788, obteve %s", ev.OrderID)
	}
	if ev.Value != 1990.00 {
		t.Errorf("esperava valor 1990, obteve %v", ev.Value)
	}
	if ev.EventName != "deal_won" {
		t.Errorf("esperava evento deal_won, obteve %s", ev.EventName)
	}
}

func TestKiwifyAdapter(t *testing.T) {
	adapter := &inbound.KiwifyAdapter{}

	payload := []byte(`{
		"order_id": "kw_998877",
		"order_status": "paid",
		"Customer": {
			"full_name": "Juliana Ferreira",
			"email": "juliana@kiwify.test",
			"mobile": "11912345678"
		},
		"Commissions": {
			"charge_amount": 49700,
			"currency": "BRL"
		},
		"tracking": {
			"src": "hn_vis_kiwify_session_abc",
			"utm_source": "meta_ads"
		}
	}`)

	ev, err := adapter.Parse(nil, payload)
	if err != nil {
		t.Fatalf("esperava sucesso no parse Kiwify: %v", err)
	}

	if ev.Email != "juliana@kiwify.test" {
		t.Errorf("esperava email juliana@kiwify.test, obteve %s", ev.Email)
	}
	if ev.OrderID != "kw_998877" {
		t.Errorf("esperava order_id kw_998877, obteve %s", ev.OrderID)
	}
	// 49700 centavos -> 497.00
	if ev.Value != 497.00 {
		t.Errorf("esperava valor normalizado de 497.00, obteve %v", ev.Value)
	}
	if ev.VisitorID != "hn_vis_kiwify_session_abc" {
		t.Errorf("esperava visitor_id hn_vis_kiwify_session_abc, obteve %s", ev.VisitorID)
	}
}

