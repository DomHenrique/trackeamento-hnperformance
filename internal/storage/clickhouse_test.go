package storage

import (
	"strings"
	"testing"
	"time"
)

func TestLeadsReportQuery_ContainsFormSubmitAndLifecycle(t *testing.T) {
	query := LeadsReportQuery

	// 1. Deve cobrir conversões canônicas
	if !strings.Contains(query, "'lead', 'whatsapp_click', 'purchase'") {
		t.Errorf("LeadsReportQuery deve conter ('lead', 'whatsapp_click', 'purchase')")
	}

	// 2. Deve cobrir form_submit com ciclo de vida validado
	if !strings.Contains(query, "event_name = 'form_submit'") {
		t.Errorf("LeadsReportQuery deve conter event_name = 'form_submit'")
	}
	if !strings.Contains(query, "form_lifecycle_state IN ('form_submit_success', 'form_client_validated')") {
		t.Errorf("LeadsReportQuery deve filtrar ciclos de vida válidos form_submit_success e form_client_validated")
	}

	// 3. Deve filtrar bots
	if !strings.Contains(query, "is_bot = 0") {
		t.Errorf("LeadsReportQuery deve filtrar robôs com is_bot = 0")
	}
}

func TestAuditLeadIntegrity(t *testing.T) {
	tests := []struct {
		name          string
		record        LeadRecord
		expectedLevel string
		expectedAlert string
	}{
		{
			name: "Conversão de formulário completa com UTM e GCLID",
			record: LeadRecord{
				EventName:  "form_submit",
				PageURL:    "https://exemplo.com/contato",
				UTMSource:  "google",
				UTMCampaign: "campanha_leads",
				GCLID:      "gclid_12345",
				EventTime:  time.Now(),
			},
			expectedLevel: "ok",
			expectedAlert: "",
		},
		{
			name: "Conversão de formulário sem ClickID (médio aviso)",
			record: LeadRecord{
				EventName:  "form_submit",
				PageURL:    "https://exemplo.com/contato",
				UTMSource:  "organico",
				UTMCampaign: "blog",
				EventTime:  time.Now(),
			},
			expectedLevel: "warning_medium",
			expectedAlert: "Sem Click ID Ads",
		},
		{
			name: "Conversão direta sem UTM e sem ClickID (crítico)",
			record: LeadRecord{
				EventName: "form_submit",
				PageURL:   "https://exemplo.com/contato",
				EventTime: time.Now(),
			},
			expectedLevel: "warning_high",
			expectedAlert: "Sem Campanha/UTMs",
		},
		{
			name: "Conversão sem URL de origem",
			record: LeadRecord{
				EventName:  "form_submit",
				UTMSource:  "facebook",
				FBCLID:     "fbclid_789",
				EventTime:  time.Now(),
			},
			expectedLevel: "warning_medium",
			expectedAlert: "Sem URL de Origem",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := tt.record
			AuditLeadIntegrity(&rec)

			if rec.IntegrityLevel != tt.expectedLevel {
				t.Errorf("IntegrityLevel = %q, esperado %q", rec.IntegrityLevel, tt.expectedLevel)
			}

			if tt.expectedAlert != "" {
				found := false
				for _, alert := range rec.MissingAlerts {
					if alert == tt.expectedAlert {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Alerta esperado %q não encontrado em %v", tt.expectedAlert, rec.MissingAlerts)
				}
			}
		})
	}
}

func TestMonitoredPagesReportQuery_CanonicalAndTitleHardening(t *testing.T) {
	// 1. Testa a query de agregação de páginas
	queryPages := MonitoredPagesQueryPages

	if !strings.Contains(queryPages, "if(page_key != '', page_key,") {
		t.Errorf("MonitoredPagesQueryPages deve utilizar fallback para page_key canônica")
	}

	if !strings.Contains(queryPages, "argMaxIf(") {
		t.Errorf("MonitoredPagesQueryPages deve usar argMaxIf para extração resiliente de título")
	}

	if !strings.Contains(queryPages, "length(trimBoth(JSONExtractString(custom_data_json, 'page_title'))) > 0") {
		t.Errorf("MonitoredPagesQueryPages deve verificar que page_title não é vazio no argMaxIf")
	}

	if !strings.Contains(queryPages, "is_bot = 0") {
		t.Errorf("MonitoredPagesQueryPages deve filtrar bots")
	}

	// 2. Testa a query de série temporal
	queryTS := MonitoredPagesQueryTS

	if !strings.Contains(queryTS, "if(page_key != '', page_key,") {
		t.Errorf("MonitoredPagesQueryTS deve utilizar fallback para page_key canônica")
	}

	if !strings.Contains(queryTS, "is_bot = 0") {
		t.Errorf("MonitoredPagesQueryTS deve filtrar bots")
	}
}
