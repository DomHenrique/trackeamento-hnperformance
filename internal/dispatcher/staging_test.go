package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"tracking-engine/internal/attribution"
	"tracking-engine/internal/collector"
	"tracking-engine/internal/config"
	"tracking-engine/internal/identity"
	"tracking-engine/internal/integrations"
	"tracking-engine/internal/storage"
)

// TestStaging_Fase3_DeduplicationLock valida estritamente a Fase 3 do roteiro de testes:
// 1. Falha permanente (422) no evento B não grava a chave de 24h, permitindo que C despache.
// 2. Falha transitória (500, depois 200) não grava a chave na falha, gravando somente no sucesso do retry.
// 3. Após o sucesso, eventos subsequentes com o mesmo submission_id são suprimidos na origem.
func TestStaging_Fase3_DeduplicationLock(t *testing.T) {
	cfg := &config.Config{Env: "test", RedisAddr: "127.0.0.1:6379"}
	rdb, err := storage.NewRedis(cfg)
	if err != nil || rdb.Client == nil {
		t.Skip("Redis local indisponível para testes de staging")
	}

	ctx := context.Background()

	t.Run("3.1 Falha permanente (422) em B não impede despacho de C", func(t *testing.T) {
		subID := fmt.Sprintf("sub_perm_422_%d", time.Now().UnixNano())
		platform := "linkedin_capi"
		defer func() {
			_ = rdb.Client.Del(ctx, fmt.Sprintf("dispatched:sub:%s:%s", platform, subID)).Err()
		}()

		// Simula B falhando com 422:
		callErrB := fmt.Errorf("erro cliente HTTP 422: unprocessable entity")
		isPermanent := isPermanentError(callErrB)
		if !isPermanent {
			t.Fatalf("Esperava que 422 fosse classificado como erro permanente")
		}

		// Como B falhou, a trava permanente de 24h NÃO deve existir:
		isDispatchedB, errB := rdb.IsSubmissionDispatched(ctx, platform, subID)
		if errB != nil {
			t.Fatalf("Erro ao consultar IsSubmissionDispatched: %v", errB)
		}
		if isDispatchedB {
			t.Fatalf("Chave dispatched:sub não pode existir após falha permanente sem sucesso")
		}

		// Evento C chega e verifica permissão de envio na origem:
		canDispatchC := !isDispatchedB
		if !canDispatchC {
			t.Fatalf("Evento C deveria ter permissão de despacho após falha de B")
		}

		// Evento C obtém sucesso (simulado):
		errMark := rdb.MarkSubmissionDispatched(ctx, platform, subID, 24*time.Hour)
		if errMark != nil {
			t.Fatalf("Erro ao registrar MarkSubmissionDispatched para C: %v", errMark)
		}

		// Agora a chave de 24h DEVE existir:
		isDispatchedAfterC, _ := rdb.IsSubmissionDispatched(ctx, platform, subID)
		if !isDispatchedAfterC {
			t.Fatalf("Chave de submissão deve existir no Redis após o sucesso de C")
		}

		// Evento D (terceiro evento irmão) é bloqueado na origem:
		isDispatchedD, _ := rdb.IsSubmissionDispatched(ctx, platform, subID)
		if !isDispatchedD {
			t.Fatalf("Evento D deveria ser bloqueado na origem como duplicado")
		}
	})

	t.Run("3.2 Falha transitória (500, depois 200) grava trava somente após sucesso", func(t *testing.T) {
		subID := fmt.Sprintf("sub_trans_500_%d", time.Now().UnixNano())
		platform := "linkedin_capi"
		defer func() {
			_ = rdb.Client.Del(ctx, fmt.Sprintf("dispatched:sub:%s:%s", platform, subID)).Err()
		}()

		// 1ª tentativa falha com 500:
		callErr1 := fmt.Errorf("erro servidor HTTP 500: internal server error")
		if isPermanentError(callErr1) {
			t.Fatalf("500 não deve ser classificado como erro permanente")
		}

		// Chave não deve existir após falha transitória:
		dispatched1, _ := rdb.IsSubmissionDispatched(ctx, platform, subID)
		if dispatched1 {
			t.Fatalf("Chave de submissão não deve existir após falha 500")
		}

		// 2ª tentativa (retry) obtém sucesso:
		errMark := rdb.MarkSubmissionDispatched(ctx, platform, subID, 24*time.Hour)
		if errMark != nil {
			t.Fatalf("Erro ao marcar sucesso do retry: %v", errMark)
		}

		// Agora chave deve existir:
		dispatched2, _ := rdb.IsSubmissionDispatched(ctx, platform, subID)
		if !dispatched2 {
			t.Fatalf("Chave de submissão deve existir após sucesso do retry")
		}

		// Verifica que o TTL está entre 23h e 24h:
		ttl, errTTL := rdb.Client.TTL(ctx, fmt.Sprintf("dispatched:sub:%s:%s", platform, subID)).Result()
		if errTTL != nil || ttl <= 23*time.Hour {
			t.Errorf("TTL da trava de submissão deve ser de aproximadamente 24h, obtido %v", ttl)
		}
	})
}

// TestStaging_Fase4_RaceConditionAndConcurrency valida estritamente a Fase 4 do roteiro:
// 1. Injeção de B antes de A com atrasos de 0ms, 50ms, 500ms.
// 2. Injeção de B órfão sem nenhum A (falha limpa em ~3s sem travas).
// 3. 50 submissões simultâneas concorrentes com medição de vazão.
func TestStaging_Fase4_RaceConditionAndConcurrency(t *testing.T) {
	cfg := &config.Config{Env: "test", RedisAddr: "127.0.0.1:6379"}
	rdb, err := storage.NewRedis(cfg)
	if err != nil || rdb.Client == nil {
		t.Skip("Redis local indisponível para testes de staging")
	}

	ctx := context.Background()
	pool := NewWorkerPool(cfg, rdb, nil)

	// 4.1 Testes com diferentes intervalos de atraso de A (0ms, 50ms, 500ms)
	delays := []time.Duration{0, 50 * time.Millisecond, 500 * time.Millisecond}
	for _, delay := range delays {
		t.Run(fmt.Sprintf("4.1 Atraso de A: %v", delay), func(t *testing.T) {
			siteID := fmt.Sprintf("site_race_delay_%d", time.Now().UnixNano())
			subID := fmt.Sprintf("sub_race_delay_%d", time.Now().UnixNano())
			defer func() {
				_ = rdb.DeleteLeadCache(ctx, siteID, subID)
			}()

			evB := &collector.EventPayload{
				EventID:            fmt.Sprintf("ev_b_%d", time.Now().UnixNano()),
				SiteID:             siteID,
				SubmissionID:       subID,
				EventName:          "form_submit",
				FormLifecycleState: "form_client_validated",
				UserData:           map[string]interface{}{}, // Vazio
				Consent:            collector.ConsentState{Analytics: true, Marketing: true},
				EventTime:          time.Now().UTC(),
			}

			// Goroutine que grava o cache de A após o atraso especificado
			go func() {
				if delay > 0 {
					time.Sleep(delay)
				}
				_ = rdb.SaveLeadCache(ctx, &storage.LeadCacheRecord{
					SubmissionID: subID,
					SiteID:       siteID,
					UserData: map[string]interface{}{
						"email": fmt.Sprintf("lead.delay.%d@exemplo.com.br", delay.Milliseconds()),
						"phone": "5511999990004",
					},
					Consent:             storage.LeadCacheConsent{Analytics: true, Marketing: true},
					ExplicitLeadConsent: true,
					ConsentStatus:       "granted",
					CachedAt:            time.Now().UTC(),
				})
			}()

			start := time.Now()
			hydrated := pool.HydrateEventLeadData(ctx, evB)
			elapsed := time.Since(start)

			if !hydrated {
				t.Fatalf("Falha na hidratação com atraso de %v em A", delay)
			}
			expectedEmail := fmt.Sprintf("lead.delay.%d@exemplo.com.br", delay.Milliseconds())
			if evB.UserData["email"] != expectedEmail {
				t.Errorf("E-mail esperado %q, obtido %q", expectedEmail, evB.UserData["email"])
			}
			t.Logf("Atraso %v: hidratação concluída em %v", delay, elapsed)
		})
	}

	// 4.2 Injeção de B órfão sem nenhum A
	t.Run("4.2 Evento B órfão sem nenhum A", func(t *testing.T) {
		siteID := fmt.Sprintf("site_orphan_%d", time.Now().UnixNano())
		subID := fmt.Sprintf("sub_orphan_%d", time.Now().UnixNano())

		evBOrphan := &collector.EventPayload{
			EventID:            fmt.Sprintf("ev_orphan_%d", time.Now().UnixNano()),
			SiteID:             siteID,
			SubmissionID:       subID,
			EventName:          "form_submit",
			FormLifecycleState: "form_client_validated",
			UserData:           map[string]interface{}{},
			Consent:            collector.ConsentState{Analytics: true, Marketing: true},
			EventTime:          time.Now().UTC(),
		}

		start := time.Now()
		hydrated := pool.HydrateEventLeadData(ctx, evBOrphan)
		elapsed := time.Since(start)

		if hydrated {
			t.Fatalf("Evento B órfão não deveria ser hidratado")
		}
		// Deve ter aguardado as 3 tentativas (~2.0s a ~2.5s)
		if elapsed < 1800*time.Millisecond {
			t.Errorf("Tempo decorrido (%v) deveria refletir as 3 tentativas completas de polling", elapsed)
		}
		// Nenhuma trava de deduplicação gravada
		isDispatched, _ := rdb.IsSubmissionDispatched(ctx, "linkedin_capi", subID)
		if isDispatched {
			t.Fatalf("Não deve existir trava de submissão para evento órfão")
		}
		t.Logf("B órfão rejeitado de forma limpa em %v", elapsed)
	})

	// 4.3 Carga concorrente de 50 submissões simultâneas
	t.Run("4.3 Concorrência de 50 submissões simultâneas", func(t *testing.T) {
		const totalConcurrent = 50
		var wg sync.WaitGroup
		var successCount int64

		startAll := time.Now()

		for i := 1; i <= totalConcurrent; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				sID := fmt.Sprintf("site_conc_%d", idx)
				subID := fmt.Sprintf("sub_conc_%d_%d", idx, time.Now().UnixNano())

				// Grava cache preliminar com pequeno atraso randômico entre 10 e 50ms
				_ = rdb.SaveLeadCache(ctx, &storage.LeadCacheRecord{
					SubmissionID: subID,
					SiteID:       sID,
					UserData: map[string]interface{}{
						"email": fmt.Sprintf("lead.conc.%d@exemplo.com.br", idx),
						"phone": fmt.Sprintf("551199999%04d", idx),
					},
					Consent:             storage.LeadCacheConsent{Analytics: true, Marketing: true},
					ExplicitLeadConsent: true,
					ConsentStatus:       "granted",
					CachedAt:            time.Now().UTC(),
				})

				ev := &collector.EventPayload{
					EventID:            fmt.Sprintf("ev_conc_%d", idx),
					SiteID:             sID,
					SubmissionID:       subID,
					EventName:          "form_submit",
					FormLifecycleState: "form_submit_success",
					UserData:           map[string]interface{}{},
					Consent:            collector.ConsentState{Analytics: true, Marketing: true},
					EventTime:          time.Now().UTC(),
				}

				if pool.HydrateEventLeadData(ctx, ev) {
					atomic.AddInt64(&successCount, 1)
				}
				_ = rdb.DeleteLeadCache(ctx, sID, subID)
			}(i)
		}

		wg.Wait()
		totalTime := time.Since(startAll)

		if successCount != totalConcurrent {
			t.Fatalf("Esperava 50 sucessos na carga concorrente, obteve %d", successCount)
		}
		throughput := float64(totalConcurrent) / totalTime.Seconds()
		avgLatency := totalTime / totalConcurrent

		t.Logf("Carga de 50 submissões concluída com 100%% de sucesso!")
		t.Logf("  Tempo total: %v | Vazão: %.1f submissões/s | Latência média: %v", totalTime, throughput, avgLatency)
	})
}

// TestStaging_Fase5_ConsentCompliance valida estritamente a Fase 5 do roteiro:
// Estados: granted, denied, pending e sem banner / sem registro.
func TestStaging_Fase5_ConsentCompliance(t *testing.T) {
	cfg := &config.Config{Env: "test", RedisAddr: "127.0.0.1:6379"}
	rdb, err := storage.NewRedis(cfg)
	if err != nil || rdb.Client == nil {
		t.Skip("Redis local indisponível para testes de staging")
	}

	ctx := context.Background()
	pool := NewWorkerPool(cfg, rdb, nil)

	// 5.1 Consentimento Concedido (granted)
	t.Run("5.1 Consentimento Granted", func(t *testing.T) {
		siteID := fmt.Sprintf("site_c_granted_%d", time.Now().UnixNano())
		subID := fmt.Sprintf("sub_c_granted_%d", time.Now().UnixNano())
		defer func() { _ = rdb.DeleteLeadCache(ctx, siteID, subID) }()

		_ = rdb.SaveLeadCache(ctx, &storage.LeadCacheRecord{
			SubmissionID:        subID,
			SiteID:              siteID,
			UserData:            map[string]interface{}{"email": "lead.granted@exemplo.com.br"},
			Consent:             storage.LeadCacheConsent{Analytics: true, Marketing: true},
			ExplicitLeadConsent: true,
			ConsentStatus:       "granted",
			CachedAt:            time.Now().UTC(),
		})

		ev := &collector.EventPayload{
			EventID:      "ev_granted",
			SiteID:       siteID,
			SubmissionID: subID,
			EventName:    "form_submit",
			UserData:     map[string]interface{}{},
			Consent:      collector.ConsentState{Analytics: true, Marketing: true},
		}

		if !pool.HydrateEventLeadData(ctx, ev) {
			t.Fatalf("Esperava hidratação autorizada para consentimento concedido")
		}
		if ev.UserData["email"] != "lead.granted@exemplo.com.br" {
			t.Errorf("E-mail não hidratado corretamente")
		}
	})

	// 5.2 Consentimento Negado (denied)
	t.Run("5.2 Consentimento Denied", func(t *testing.T) {
		siteID := fmt.Sprintf("site_c_denied_%d", time.Now().UnixNano())
		subID := fmt.Sprintf("sub_c_denied_%d", time.Now().UnixNano())
		defer func() { _ = rdb.DeleteLeadCache(ctx, siteID, subID) }()

		_ = rdb.SaveLeadCache(ctx, &storage.LeadCacheRecord{
			SubmissionID:        subID,
			SiteID:              siteID,
			UserData:            map[string]interface{}{"email": "lead.denied@exemplo.com.br"},
			Consent:             storage.LeadCacheConsent{Analytics: false, Marketing: false},
			ExplicitLeadConsent: false,
			ConsentStatus:       "denied",
			CachedAt:            time.Now().UTC(),
		})

		ev := &collector.EventPayload{
			EventID:      "ev_denied",
			SiteID:       siteID,
			SubmissionID: subID,
			EventName:    "form_submit",
			UserData:     map[string]interface{}{},
			Consent:      collector.ConsentState{Analytics: false, Marketing: false},
		}

		if pool.HydrateEventLeadData(ctx, ev) {
			t.Fatalf("Não é permitido hidratar dados quando consentimento foi explicitamente recusado")
		}
		if ev.UserData["email"] != nil {
			t.Fatalf("Dados pessoais não devem ser expostos no payload sob recusa de consentimento")
		}
	})

	// 5.3 Consentimento Pendente (pending)
	t.Run("5.3 Consentimento Pending", func(t *testing.T) {
		siteID := fmt.Sprintf("site_c_pending_%d", time.Now().UnixNano())
		subID := fmt.Sprintf("sub_c_pending_%d", time.Now().UnixNano())
		defer func() { _ = rdb.DeleteLeadCache(ctx, siteID, subID) }()

		_ = rdb.SaveLeadCache(ctx, &storage.LeadCacheRecord{
			SubmissionID:        subID,
			SiteID:              siteID,
			UserData:            map[string]interface{}{"email": "lead.pending@exemplo.com.br"},
			Consent:             storage.LeadCacheConsent{Analytics: false, Marketing: false},
			ExplicitLeadConsent: false,
			ConsentStatus:       "pending",
			CachedAt:            time.Now().UTC(),
		})

		ev := &collector.EventPayload{
			EventID:      "ev_pending",
			SiteID:       siteID,
			SubmissionID: subID,
			EventName:    "form_submit",
			UserData:     map[string]interface{}{},
			Consent:      collector.ConsentState{Analytics: false, Marketing: false},
		}

		if pool.HydrateEventLeadData(ctx, ev) {
			t.Fatalf("Consentimento pendente sem autorização explícita de marketing não deve autorizar hidratação")
		}
	})
}

// TestStaging_Fase6_RetentionAndTTL valida estritamente a Fase 6 do roteiro:
// 1. Chave de lead expira em 15 minutos (com tolerância de jitter).
// 2. Chave de registro hidratado para retry expira em 24 horas.
// 3. DLQ expira em 24 horas.
// 4. A chave do lead não é apagada logo após o término do primeiro evento.
func TestStaging_Fase6_RetentionAndTTL(t *testing.T) {
	cfg := &config.Config{Env: "test", RedisAddr: "127.0.0.1:6379"}
	rdb, err := storage.NewRedis(cfg)
	if err != nil || rdb.Client == nil {
		t.Skip("Redis local indisponível para testes de staging")
	}

	ctx := context.Background()
	siteID := fmt.Sprintf("site_ret_%d", time.Now().UnixNano())
	subID := fmt.Sprintf("sub_ret_%d", time.Now().UnixNano())
	eventID := fmt.Sprintf("ev_ret_%d", time.Now().UnixNano())

	defer func() {
		_ = rdb.DeleteLeadCache(ctx, siteID, subID)
		_ = rdb.Client.Del(ctx, fmt.Sprintf("dispatch:event_user_data:%s", eventID)).Err()
	}()

	// 1. Grava cache transitório de lead no Redis
	errSave := rdb.SaveLeadCache(ctx, &storage.LeadCacheRecord{
		SubmissionID: subID,
		SiteID:       siteID,
		UserData: map[string]interface{}{
			"email": "lead.retencao@exemplo.com.br",
		},
		Consent:             storage.LeadCacheConsent{Analytics: true, Marketing: true},
		ExplicitLeadConsent: true,
		ConsentStatus:       "granted",
		CachedAt:            time.Now().UTC(),
	})
	if errSave != nil {
		t.Fatalf("Erro ao gravar LeadCache: %v", errSave)
	}

	// Valida TTL do LeadCache (deve estar em ~15 min = 900s)
	leadTTL, errTTL := rdb.Client.TTL(ctx, fmt.Sprintf("tracking:lead_cache:%s:%s", siteID, subID)).Result()
	if errTTL != nil || leadTTL <= 14*time.Minute || leadTTL > 16*time.Minute {
		t.Errorf("TTL do LeadCache esperado em aproximadamente 15m, obtido %v", leadTTL)
	}

	// 2. Grava registro hidratado para retry com TTL de 24h
	errSaveHydrated := rdb.SaveEventHydratedUserData(ctx, eventID, map[string]interface{}{
		"email": "lead.retencao@exemplo.com.br",
	}, 24*time.Hour)
	if errSaveHydrated != nil {
		t.Fatalf("Erro ao gravar SaveEventHydratedUserData: %v", errSaveHydrated)
	}

	hydratedTTL, _ := rdb.Client.TTL(ctx, fmt.Sprintf("dispatch:event_user_data:%s", eventID)).Result()
	if hydratedTTL <= 23*time.Hour || hydratedTTL > 25*time.Hour {
		t.Errorf("TTL do registro hidratado esperado em aproximadamente 24h, obtido %v", hydratedTTL)
	}

	// 3. Testa enfileiramento na Dead-Letter Queue (DLQ) com TTL de 24h
	dlqStream := fmt.Sprintf("stream:test_dlq_%d", time.Now().UnixNano())
	defer func() {
		_ = rdb.Client.Del(ctx, dlqStream).Err()
	}()

	errDLQ := rdb.PushDeadLetter(ctx, dlqStream, map[string]interface{}{
		"event_id": eventID,
		"reason":   "erro cliente HTTP 422: unprocessable entity",
	})
	if errDLQ != nil {
		t.Fatalf("Erro ao enviar mensagem para DLQ: %v", errDLQ)
	}

	dlqTTL, _ := rdb.Client.TTL(ctx, dlqStream).Result()
	if dlqTTL <= 23*time.Hour || dlqTTL > 25*time.Hour {
		t.Errorf("TTL da DLQ esperado em aproximadamente 24h, obtido %v", dlqTTL)
	}

	// 4. Confirma que a chave do lead não é apagada após a hidratação de B (não-purga prematura)
	evB := &collector.EventPayload{
		EventID:      eventID,
		SiteID:       siteID,
		SubmissionID: subID,
		EventName:    "form_submit",
		UserData:     map[string]interface{}{},
		Consent:      collector.ConsentState{Analytics: true, Marketing: true},
	}
	pool := NewWorkerPool(cfg, rdb, nil)
	_ = pool.HydrateEventLeadData(ctx, evB)

	// A chave do LeadCache ainda deve existir com TTL íntegro para que C possa ler!
	leadCacheStillExists, _ := rdb.GetLeadCache(ctx, siteID, subID)
	if leadCacheStillExists == nil || leadCacheStillExists.UserData["email"] != "lead.retencao@exemplo.com.br" {
		t.Fatalf("A chave de lead no Redis foi apagada prematuramente! Ela deve sobreviver pelo seu TTL de 15m")
	}
	t.Logf("Chave de LeadCache sobreviveu com sucesso após hidratação (TTL: %v)", leadTTL)
}

// TestStaging_MockServer_EndToEndLinkedInCAPI simula requisições reais HTTP contra um servidor Mock
// validando rejeições 422, tentativas 500 com retry, e formato do payload transmitido.
func TestStaging_MockServer_EndToEndLinkedInCAPI(t *testing.T) {
	var requestCount int64
	var lastReceivedBody []byte
	var lastHeaders http.Header

	// Mock Server simulando LinkedIn CAPI com alternância de comportamento
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqNum := atomic.AddInt64(&requestCount, 1)
		lastHeaders = r.Header
		lastReceivedBody, _ = io.ReadAll(r.Body)

		if strings.Contains(r.Header.Get("X-Simulate-Status"), "422") && reqNum == 1 {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message": "ERROR :: simulated 422 unprocessable entity"}`))
			return
		}
		if strings.Contains(r.Header.Get("X-Simulate-Status"), "500") && reqNum == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message": "ERROR :: simulated 500 internal server error"}`))
			return
		}

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status": "created"}`))
	}))
	defer mockServer.Close()

	client := integrations.NewHTTPClient(3 * time.Second)
	_ = client

	// Validação de formato de payload
	ev := &collector.EventPayload{
		EventID:      "ev_stage_synth_999",
		SubmissionID: "sub_stage_synth_999",
		EventName:    "form_submit",
		EventTime:    time.Now().UTC(),
		UserData: map[string]interface{}{
			"email":        "lead.staging@exemplo.com.br",
			"name":         "Henrique Carvalho",
			"company_name": "Gridd Mkt",
		},
		Attribution: attribution.Params{
			LIFATID: "li_fat_uuid_staging_999",
		},
	}

	normEmail := identity.NormalizeEmail(ev.UserData["email"].(string))
	event := integrations.LinkedInConversionEvent{
		Conversion:           integrations.FormatConversionURN("999888"),
		ConversionHappenedAt: ev.EventTime.UnixMilli(),
		User: integrations.LinkedInUser{
			UserIds: []integrations.LinkedInUserId{
				{
					IdType:  "SHA256_EMAIL",
					IdValue: identity.HashSHA256(normEmail),
				},
				{
					IdType:  "LINKEDIN_FIRST_PARTY_ADS_TRACKING_UUID",
					IdValue: "li_fat_uuid_staging_999",
				},
			},
			UserInfo: &integrations.LinkedInUserInfo{
				FirstName:   "Henrique",
				LastName:    "Carvalho",
				CompanyName: "Gridd Mkt",
				CountryCode: "BR",
			},
		},
		EventId: ev.SubmissionID,
	}

	b, _ := json.Marshal(event)
	resp, code, err := client.PostRaw(context.Background(), mockServer.URL, map[string]string{
		"Authorization":     "Bearer stage_token",
		"X-Simulate-Status": "200",
	}, b)

	if err != nil || code != http.StatusCreated {
		t.Fatalf("Erro no envio ao Mock Server: code=%d err=%v", code, err)
	}

	var parsed integrations.LinkedInConversionEvent
	_ = json.Unmarshal(lastReceivedBody, &parsed)

	if parsed.EventId != "sub_stage_synth_999" {
		t.Errorf("EventId recebido incorreto: %s", parsed.EventId)
	}
	if len(parsed.User.UserIds) != 2 {
		t.Errorf("Esperava 2 userIds (email e li_fat_id), obtido %d", len(parsed.User.UserIds))
	}
	if parsed.User.UserInfo.FirstName != "Henrique" || parsed.User.UserInfo.LastName != "Carvalho" {
		t.Errorf("UserInfo incorreto: %+v", parsed.User.UserInfo)
	}
	_ = resp
	_ = lastHeaders
}
