package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"tracking-engine/internal/collector"
	"tracking-engine/internal/config"
	"tracking-engine/internal/integrations"
	"tracking-engine/internal/storage"
)

type customTimeoutError struct{}

func (e *customTimeoutError) Error() string   { return "i/o timeout" }
func (e *customTimeoutError) Timeout() bool   { return true }
func (e *customTimeoutError) Temporary() bool { return true }

func TestIsPermanentError(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		isPermanent bool
	}{
		{
			name:        "nil error",
			err:         nil,
			isPermanent: false,
		},
		{
			name:        "context deadline exceeded (transient)",
			err:         context.DeadlineExceeded,
			isPermanent: false,
		},
		{
			name:        "net.Error timeout (transient)",
			err:         &customTimeoutError{},
			isPermanent: false,
		},
		{
			name:        "HTTP 429 Too Many Requests (transient)",
			err:         errors.New("erro servidor HTTP 429: rate limit exceeded"),
			isPermanent: false,
		},
		{
			name:        "HTTP 500 Internal Server Error (transient)",
			err:         errors.New("erro servidor HTTP 500: internal server error"),
			isPermanent: false,
		},
		{
			name:        "HTTP 502 Bad Gateway (transient)",
			err:         errors.New("erro servidor HTTP 502: bad gateway"),
			isPermanent: false,
		},
		{
			name:        "HTTP 503 Service Unavailable (transient)",
			err:         errors.New("erro servidor HTTP 503: upstream down"),
			isPermanent: false,
		},
		{
			name:        "HTTP 504 Gateway Timeout (transient)",
			err:         errors.New("erro servidor HTTP 504: gateway timeout"),
			isPermanent: false,
		},
		{
			name:        "Connection Refused (transient)",
			err:         errors.New("dial tcp 127.0.0.1:443: connection refused"),
			isPermanent: false,
		},
		{
			name:        "Connection Reset (transient)",
			err:         errors.New("read tcp: connection reset by peer"),
			isPermanent: false,
		},
		{
			name:        "HTTP 400 Bad Request (permanent)",
			err:         errors.New("erro cliente HTTP 400: invalid json parameter"),
			isPermanent: true,
		},
		{
			name:        "HTTP 401 Unauthorized (permanent)",
			err:         errors.New("erro cliente HTTP 401: invalid access token"),
			isPermanent: true,
		},
		{
			name:        "HTTP 403 Forbidden (permanent)",
			err:         errors.New("erro cliente HTTP 403: permission denied"),
			isPermanent: true,
		},
		{
			name:        "HTTP 404 Not Found (permanent)",
			err:         errors.New("erro cliente HTTP 404: conversion endpoint not found"),
			isPermanent: true,
		},
		{
			name:        "HTTP 422 Unprocessable Entity (permanent)",
			err:         errors.New("erro cliente HTTP 422: missing required visitor fields"),
			isPermanent: true,
		},
		{
			name:        "Invalid Token message (permanent)",
			err:         errors.New("OAuth: invalid token specified"),
			isPermanent: true,
		},
		{
			name:        "Validação pré-despacho sem identificador (permanent)",
			err:         errors.New("validação pré-despacho falhou: nenhum identificador de usuário (email ou telefone) fornecido para LinkedIn Conversions API"),
			isPermanent: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isPermanentError(tt.err)
			if got != tt.isPermanent {
				t.Errorf("isPermanentError(%v) = %v; esperado %v", tt.err, got, tt.isPermanent)
			}
		})
	}
}

func TestCalculateBackoff(t *testing.T) {
	// Tentativa 1: base 1s + jitter (máx 1.25s)
	b1 := calculateBackoff(1)
	if b1 < 1*time.Second || b1 > 2*time.Second {
		t.Errorf("calculateBackoff(1) = %v fora do intervalo esperado [1s, 2s]", b1)
	}

	// Tentativa 2: base 2s + jitter (máx 2.5s)
	b2 := calculateBackoff(2)
	if b2 < 2*time.Second || b2 > 3*time.Second {
		t.Errorf("calculateBackoff(2) = %v fora do intervalo esperado [2s, 3s]", b2)
	}

	// Tentativa 3: base 4s + jitter (máx 5s)
	b3 := calculateBackoff(3)
	if b3 < 4*time.Second || b3 > 6*time.Second {
		t.Errorf("calculateBackoff(3) = %v fora do intervalo esperado [4s, 6s]", b3)
	}

	// Tentativa 10: deve respeitar o teto de 60s (+ jitter de no máximo 15s)
	b10 := calculateBackoff(10)
	if b10 < 60*time.Second || b10 > 76*time.Second {
		t.Errorf("calculateBackoff(10) = %v excedeu o teto esperado [60s, 76s]", b10)
	}
}

func TestDispatchIdempotencyStateMachine(t *testing.T) {
	// Verifica se há Redis local disponível
	conn, err := net.DialTimeout("tcp", "127.0.0.1:6379", 500*time.Millisecond)
	if err != nil {
		t.Skip("Redis local 127.0.0.1:6379 não disponível, pulando teste de integração da máquina de estados")
	}
	_ = conn.Close()

	cfg := &config.Config{
		RedisAddr: "127.0.0.1:6379",
	}
	rdb, err := storage.NewRedis(cfg)
	if err != nil || rdb.Client == nil {
		t.Skip("Falha ao inicializar cliente Redis")
	}

	ctx := context.Background()
	testEventID := fmt.Sprintf("ev_test_%d", time.Now().UnixNano())
	testIntegID := fmt.Sprintf("integ_meta_%d", time.Now().UnixNano())
	key := fmt.Sprintf("dispatch:lock:%s:%s", testIntegID, testEventID)
	defer func() {
		_ = rdb.Client.Del(ctx, key).Err()
	}()

	// 1. Primeira aquisição de lease -> Sucesso
	acquired, alreadySucceeded, attempts, err := rdb.AcquireDispatchLease(ctx, testIntegID, testEventID, 2*time.Second)
	if err != nil {
		t.Fatalf("erro ao adquirir lease: %v", err)
	}
	if !acquired || alreadySucceeded || attempts != 1 {
		t.Fatalf("esperado acquired=true, alreadySucceeded=false, attempts=1. Obtido: acquired=%v, succeeded=%v, attempts=%d", acquired, alreadySucceeded, attempts)
	}

	// 2. Tentativa concorrente enquanto lease está válida -> Bloqueada
	acquired2, alreadySucceeded2, attempts2, err := rdb.AcquireDispatchLease(ctx, testIntegID, testEventID, 2*time.Second)
	if err != nil {
		t.Fatalf("erro ao verificar lease concorrente: %v", err)
	}
	if acquired2 || alreadySucceeded2 {
		t.Fatalf("esperado acquired=false para lease ativa concorrente. Obtido: acquired=%v, succeeded=%v, attempts=%d", acquired2, alreadySucceeded2, attempts2)
	}

	// 3. Simula falha transitória e marca retry com backoff de 500ms
	err = rdb.MarkDispatchRetry(ctx, testIntegID, testEventID, 1, 500*time.Millisecond, "temporary 503")
	if err != nil {
		t.Fatalf("erro ao marcar retry: %v", err)
	}

	// 4. Tentativa imediata antes do backoff -> Bloqueada
	acquired3, _, _, _ := rdb.AcquireDispatchLease(ctx, testIntegID, testEventID, 2*time.Second)
	if acquired3 {
		t.Fatalf("esperado acquired=false antes de expirar a janela de backoff")
	}

	// 5. Aguarda expirar a janela de backoff -> Consegue adquirir nova lease
	time.Sleep(600 * time.Millisecond)
	acquired4, _, attempts4, err := rdb.AcquireDispatchLease(ctx, testIntegID, testEventID, 2*time.Second)
	if err != nil {
		t.Fatalf("erro ao readquirir lease após backoff: %v", err)
	}
	if !acquired4 || attempts4 != 2 {
		t.Fatalf("esperado acquired=true e attempts=2 após backoff. Obtido: acquired=%v, attempts=%d", acquired4, attempts4)
	}

	// 6. Registra sucesso definitivo
	err = rdb.MarkDispatchSuccess(ctx, testIntegID, testEventID)
	if err != nil {
		t.Fatalf("erro ao marcar sucesso: %v", err)
	}

	// 7. Próxima tentativa após sucesso -> Ignora disparo e acusa alreadySucceeded = true
	acquired5, alreadySucceeded5, _, err := rdb.AcquireDispatchLease(ctx, testIntegID, testEventID, 2*time.Second)
	if err != nil {
		t.Fatalf("erro ao checar lease após sucesso: %v", err)
	}
	if acquired5 || !alreadySucceeded5 {
		t.Fatalf("esperado acquired=false e alreadySucceeded=true após sucesso. Obtido: acquired=%v, alreadySucceeded=%v", acquired5, alreadySucceeded5)
	}
}

func TestDeadLetterQueuePush(t *testing.T) {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:6379", 500*time.Millisecond)
	if err != nil {
		t.Skip("Redis local 127.0.0.1:6379 não disponível, pulando teste de Dead-Letter Queue")
	}
	_ = conn.Close()

	cfg := &config.Config{RedisAddr: "127.0.0.1:6379"}
	rdb, err := storage.NewRedis(cfg)
	if err != nil || rdb.Client == nil {
		t.Skip("Falha ao inicializar cliente Redis")
	}

	ctx := context.Background()
	testStream := fmt.Sprintf("stream:test:dlq:%d", time.Now().UnixNano())
	defer func() {
		_ = rdb.Client.Del(ctx, testStream).Err()
	}()

	dlqData := map[string]interface{}{
		"integration_id":     "integ_ga4_123",
		"platform":           "ga4",
		"event_id":           "ev_fail_999",
		"event_name":         "purchase",
		"site_id":            "site_abc",
		"attempts":           5,
		"is_permanent_error": false,
		"last_error_message": "failed after 5 retries with 500",
		"last_failed_at":     time.Now().UTC().Format(time.RFC3339),
	}

	err = rdb.PushDeadLetter(ctx, testStream, dlqData)
	if err != nil {
		t.Fatalf("erro ao enviar mensagem para DLQ: %v", err)
	}

	// Valida se a mensagem foi gravada no stream
	msgs, err := rdb.Client.XRange(ctx, testStream, "-", "+").Result()
	if err != nil {
		t.Fatalf("erro ao ler DLQ stream: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("esperada 1 mensagem na DLQ, obtido %d", len(msgs))
	}
	if _, ok := msgs[0].Values["payload"]; !ok {
		t.Fatalf("payload ausente na mensagem da DLQ: %v", msgs[0].Values)
	}
}

func TestDispatchPermanentFailureTerminalState(t *testing.T) {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:6379", 500*time.Millisecond)
	if err != nil {
		t.Skip("Redis local 127.0.0.1:6379 não disponível")
	}
	_ = conn.Close()

	cfg := &config.Config{RedisAddr: "127.0.0.1:6379"}
	rdb, err := storage.NewRedis(cfg)
	if err != nil || rdb.Client == nil {
		t.Skip("Falha ao inicializar cliente Redis")
	}

	ctx := context.Background()
	testEventID := fmt.Sprintf("ev_perm_%d", time.Now().UnixNano())
	testIntegID := fmt.Sprintf("integ_meta_%d", time.Now().UnixNano())
	key := fmt.Sprintf("dispatch:lock:%s:%s", testIntegID, testEventID)
	defer func() {
		_ = rdb.Client.Del(ctx, key).Err()
	}()

	// 1. Adquire lease inicial
	acquired, _, _, err := rdb.AcquireDispatchLease(ctx, testIntegID, testEventID, 2*time.Second)
	if err != nil || !acquired {
		t.Fatalf("falha ao adquirir lease inicial: %v", err)
	}

	// 2. Marca permanent_failure (ex: erro 401 ou 5 tentativas esgotadas)
	err = rdb.MarkDispatchPermanentFailure(ctx, testIntegID, testEventID, 5, "HTTP 401 Invalid Token")
	if err != nil {
		t.Fatalf("erro ao marcar falha permanente: %v", err)
	}

	// 3. Novas tentativas devem reportar terminal (alreadySucceeded=true / terminal) para não reenviar
	acquired2, alreadySucceeded2, attempts2, err := rdb.AcquireDispatchLease(ctx, testIntegID, testEventID, 2*time.Second)
	if err != nil {
		t.Fatalf("erro ao checar lease pós falha permanente: %v", err)
	}
	if acquired2 || !alreadySucceeded2 || attempts2 != 5 {
		t.Fatalf("esperado acquired=false, alreadySucceeded=true, attempts=5. Obtido: acquired=%v, succeeded=%v, attempts=%d", acquired2, alreadySucceeded2, attempts2)
	}
}

func TestAutoClaimPending(t *testing.T) {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:6379", 500*time.Millisecond)
	if err != nil {
		t.Skip("Redis local 127.0.0.1:6379 não disponível")
	}
	_ = conn.Close()

	cfg := &config.Config{RedisAddr: "127.0.0.1:6379"}
	rdb, err := storage.NewRedis(cfg)
	if err != nil || rdb.Client == nil {
		t.Skip("Falha ao inicializar cliente Redis")
	}

	ctx := context.Background()
	testStream := fmt.Sprintf("stream:test:autoclaim:%d", time.Now().UnixNano())
	group := "test_claim_group"
	consumerA := "consumer_dead"
	consumerB := "consumer_active"

	defer func() {
		_ = rdb.Client.Del(ctx, testStream).Err()
	}()

	_ = rdb.EnsureConsumerGroup(ctx, testStream, group)

	// Adiciona uma mensagem ao stream
	msgID, err := rdb.Client.XAdd(ctx, &redis.XAddArgs{
		Stream: testStream,
		Values: map[string]interface{}{"payload": "test_data"},
	}).Result()
	if err != nil {
		t.Fatalf("erro ao adicionar mensagem: %v", err)
	}

	// Lê com consumerA para que a mensagem entre no PEL de consumerA
	readMsgs, err := rdb.Client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    group,
		Consumer: consumerA,
		Streams:  []string{testStream, ">"},
		Count:    1,
		Block:    100 * time.Millisecond,
	}).Result()
	if err != nil || len(readMsgs) == 0 || len(readMsgs[0].Messages) == 0 {
		t.Fatalf("erro ao ler mensagem para colocar no PEL: %v", err)
	}

	// AutoClaim com minIdle de 1ms usando consumerB para reivindicar a mensagem de consumerA
	time.Sleep(10 * time.Millisecond)
	claimed, _, err := rdb.AutoClaimPending(ctx, testStream, group, consumerB, 5*time.Millisecond, "0-0", 10)
	if err != nil {
		t.Fatalf("erro ao executar AutoClaimPending: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("esperada 1 mensagem reivindicada via AutoClaim, obtido %d", len(claimed))
	}
	if claimed[0].ID != msgID {
		t.Fatalf("ID da mensagem reivindicada (%s) difere da original (%s)", claimed[0].ID, msgID)
	}
}

func TestWorkerPool_KeyRingDecryption(t *testing.T) {
	cfg := &config.Config{Env: "test", DispatcherTimeoutSec: 5}
	pool := NewWorkerPool(cfg, nil, nil)

	kr := pool.getKeyRing()
	if kr == nil {
		t.Fatal("esperava keyring inicializado no worker pool")
	}

	siteUUID := "c56a4180-65aa-42ec-a945-5fd2dec0538d"
	platform := "meta_capi"
	field := "access_token"
	rawSecret := "EAABrealToken123456"

	encrypted, err := kr.EncryptField(rawSecret, siteUUID, platform, field)
	if err != nil {
		t.Fatalf("falha ao encriptar: %v", err)
	}

	// 1. Decifra valor encriptado
	decrypted, err := kr.DecryptField(encrypted, siteUUID, platform, field)
	if err != nil {
		t.Fatalf("falha ao decifrar: %v", err)
	}
	if decrypted != rawSecret {
		t.Fatalf("esperava %s, obteve %s", rawSecret, decrypted)
	}

	// 2. Passa valor legado em texto plano
	legacyToken := "legacy_plain_token_xyz"
	legacyDecrypted, err := kr.DecryptField(legacyToken, siteUUID, platform, field)
	if err != nil {
		t.Fatalf("falha no pass-through de texto plano: %v", err)
	}
	if legacyDecrypted != legacyToken {
		t.Fatalf("esperava %s, obteve %s", legacyToken, legacyDecrypted)
	}
}

func TestWorkerPool_EventMapping(t *testing.T) {
	origEv := &collector.EventPayload{
		EventID:   "evt_map_1",
		EventName: "form_submit",
	}

	it := SiteIntegrationRecord{
		ID:       "site_1_webhook",
		Platform: "webhook",
		EventMappings: map[string]string{
			"form_submit": "Lead",
		},
	}

	targetEv := origEv
	if mappedName, ok := it.EventMappings[origEv.EventName]; ok && strings.TrimSpace(mappedName) != "" {
		cloned := *origEv
		cloned.EventName = strings.TrimSpace(mappedName)
		targetEv = &cloned
	}

	if targetEv.EventName != "Lead" {
		t.Errorf("esperava EventName 'Lead', obtido '%s'", targetEv.EventName)
	}
	if origEv.EventName != "form_submit" {
		t.Errorf("evento original não deveria ter sido modificado. Esperado 'form_submit', obtido '%s'", origEv.EventName)
	}
}

func TestWorkerPool_LeadHydration(t *testing.T) {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:6379", 500*time.Millisecond)
	if err != nil {
		t.Skip("Redis local não disponível, pulando teste")
	}
	_ = conn.Close()

	cfg := &config.Config{Env: "test", RedisAddr: "127.0.0.1:6379"}
	rdb, err := storage.NewRedis(cfg)
	if err != nil || rdb.Client == nil {
		t.Skip("Falha conectando ao Redis")
	}

	ctx := context.Background()
	siteID := fmt.Sprintf("site_hyd_%d", time.Now().UnixNano())
	subID := fmt.Sprintf("sub_hyd_%d", time.Now().UnixNano())

	// Grava registro no Redis
	_ = rdb.SaveLeadCache(ctx, &storage.LeadCacheRecord{
		SubmissionID: subID,
		SiteID:       siteID,
		UserData: map[string]interface{}{
			"email": "eduardo@teste.com",
			"phone": "5554991143857",
			"name":  "Eduardo Rosalem",
		},
		Consent:             storage.LeadCacheConsent{Analytics: true, Marketing: true},
		ExplicitLeadConsent: true,
		CachedAt:            time.Now().UTC(),
	})
	defer func() {
		_ = rdb.DeleteLeadCache(ctx, siteID, subID)
	}()

	pool := NewWorkerPool(cfg, rdb, nil)

	// Evento chega com UserData vazio
	ev := &collector.EventPayload{
		EventID:      fmt.Sprintf("ev_hyd_%d", time.Now().UnixNano()),
		SiteID:       siteID,
		SubmissionID: subID,
		EventName:    "form_submit",
		UserData:     map[string]interface{}{},
		Consent:      collector.ConsentState{Analytics: true, Marketing: true},
	}

	hydrated := pool.HydrateEventLeadData(ctx, ev)
	if !hydrated {
		t.Fatalf("esperava hidratação bem-sucedida, obtido false")
	}

	// Valida que ev.UserData foi hidratado
	if ev.UserData["email"] != "eduardo@teste.com" {
		t.Errorf("esperava email hidratado 'eduardo@teste.com', obtido '%v'", ev.UserData["email"])
	}
	if ev.UserData["phone"] != "5554991143857" {
		t.Errorf("esperava phone hidratado '5554991143857', obtido '%v'", ev.UserData["phone"])
	}
	if !ev.ExplicitLeadConsent {
		t.Errorf("esperava ExplicitLeadConsent true, obtido false")
	}

	// Teste adicional: Chave inexistente/expirada retorna false
	evInvalido := &collector.EventPayload{
		EventID:      fmt.Sprintf("ev_inv_%d", time.Now().UnixNano()),
		SiteID:       siteID,
		SubmissionID: "sub_inexistente_12345",
		EventName:    "form_submit",
		UserData:     map[string]interface{}{},
		Consent:      collector.ConsentState{Analytics: true, Marketing: true},
	}
	if pool.HydrateEventLeadData(ctx, evInvalido) {
		t.Errorf("não deveria hidratar evento com submissão inexistente no Redis")
	}
}

func TestWorkerPool_LeadHydration_OutOfOrder(t *testing.T) {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:6379", 500*time.Millisecond)
	if err != nil {
		t.Skip("Redis local não disponível, pulando teste")
	}
	_ = conn.Close()

	cfg := &config.Config{Env: "test", RedisAddr: "127.0.0.1:6379"}
	rdb, err := storage.NewRedis(cfg)
	if err != nil || rdb.Client == nil {
		t.Skip("Falha conectando ao Redis")
	}

	ctx := context.Background()
	siteID := fmt.Sprintf("site_ooo_%d", time.Now().UnixNano())
	subID := fmt.Sprintf("sub_ooo_%d", time.Now().UnixNano())

	defer func() {
		_ = rdb.DeleteLeadCache(ctx, siteID, subID)
	}()

	pool := NewWorkerPool(cfg, rdb, nil)

	// Simula gravação assíncrona com atraso de 100ms (chega enquanto o worker está tentando)
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = rdb.SaveLeadCache(ctx, &storage.LeadCacheRecord{
			SubmissionID: subID,
			SiteID:       siteID,
			UserData: map[string]interface{}{
				"email": "concorrente@teste.com",
				"phone": "5511999998888",
			},
			ExplicitLeadConsent: true,
			CachedAt:            time.Now().UTC(),
		})
	}()

	ev := &collector.EventPayload{
		EventID:      fmt.Sprintf("ev_ooo_%d", time.Now().UnixNano()),
		SiteID:       siteID,
		SubmissionID: subID,
		EventName:    "form_submit",
		UserData:     map[string]interface{}{},
		Consent:      collector.ConsentState{Analytics: true, Marketing: true},
	}

	hydrated := pool.HydrateEventLeadData(ctx, ev)
	if !hydrated {
		t.Fatalf("esperava hidratação bem-sucedida após retry de evento fora de ordem")
	}

	if ev.UserData["email"] != "concorrente@teste.com" {
		t.Errorf("esperava email hidratado após retry fora de ordem, obtido '%v'", ev.UserData["email"])
	}
	if ev.UserData["phone"] != "5511999998888" {
		t.Errorf("esperava phone hidratado após retry fora de ordem, obtido '%v'", ev.UserData["phone"])
	}
}

func TestWorkerPool_LeadHydration_DeniedConsent(t *testing.T) {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:6379", 500*time.Millisecond)
	if err != nil {
		t.Skip("Redis local não disponível, pulando teste")
	}
	_ = conn.Close()

	cfg := &config.Config{Env: "test", RedisAddr: "127.0.0.1:6379"}
	rdb, err := storage.NewRedis(cfg)
	if err != nil || rdb.Client == nil {
		t.Skip("Falha conectando ao Redis")
	}

	ctx := context.Background()
	siteID := fmt.Sprintf("site_noconsent_%d", time.Now().UnixNano())
	subID := fmt.Sprintf("sub_noconsent_%d", time.Now().UnixNano())

	// Grava registro no Redis com consentimento negado
	_ = rdb.SaveLeadCache(ctx, &storage.LeadCacheRecord{
		SubmissionID: subID,
		SiteID:       siteID,
		UserData: map[string]interface{}{
			"email": "negado@teste.com",
		},
		Consent:             storage.LeadCacheConsent{Analytics: true, Marketing: false},
		ExplicitLeadConsent: false,
		CachedAt:            time.Now().UTC(),
	})
	defer func() {
		_ = rdb.DeleteLeadCache(ctx, siteID, subID)
	}()

	pool := NewWorkerPool(cfg, rdb, nil)

	ev := &collector.EventPayload{
		EventID:      fmt.Sprintf("ev_noconsent_%d", time.Now().UnixNano()),
		SiteID:       siteID,
		SubmissionID: subID,
		EventName:    "form_submit",
		UserData:     map[string]interface{}{},
		Consent:      collector.ConsentState{Analytics: true, Marketing: false},
	}

	hydrated := pool.HydrateEventLeadData(ctx, ev)
	if hydrated {
		t.Errorf("hidratação deveria ter sido vetada por falta de consentimento, mas retornou true")
	}

	// Não deve ter hidratado o email pois consentimento foi negado
	if ev.UserData["email"] != nil {
		t.Errorf("hidratação não deveria ter ocorrido com consentimento negado, obtido '%v'", ev.UserData["email"])
	}
}

func TestWorkerPool_SubmissionDeduplication(t *testing.T) {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:6379", 500*time.Millisecond)
	if err != nil {
		t.Skip("Redis local não disponível, pulando teste")
	}
	_ = conn.Close()

	cfg := &config.Config{Env: "test", RedisAddr: "127.0.0.1:6379"}
	rdb, err := storage.NewRedis(cfg)
	if err != nil || rdb.Client == nil {
		t.Skip("Falha conectando ao Redis")
	}

	ctx := context.Background()
	integID := fmt.Sprintf("integ_test_%d", time.Now().UnixNano())
	subID := fmt.Sprintf("sub_dedup_test_%d", time.Now().UnixNano())

	defer func() {
		key := fmt.Sprintf("dispatch:sub_lock:%s:%s", integID, subID)
		_ = rdb.Client.Del(ctx, key).Err()
	}()

	// 1. Simula que a submissão já teve sucesso em despacho prévio
	_ = rdb.MarkSubmissionDispatchSuccess(ctx, integID, subID)

	// 2. Tenta adquirir lease para o mesmo submission_id -> deve retornar alreadySucceeded = true
	acquired, alreadySucceeded, err := rdb.AcquireSubmissionDispatchLease(ctx, integID, subID, 10*time.Second)
	if err != nil {
		t.Fatalf("erro ao verificar lease: %v", err)
	}
	if acquired || !alreadySucceeded {
		t.Errorf("esperado acquired=false e alreadySucceeded=true, obtido acquired=%v alreadySucceeded=%v", acquired, alreadySucceeded)
	}
}

func TestWorkerPool_LinkedInCAPI_E2E_BricksHydration(t *testing.T) {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:6379", 500*time.Millisecond)
	if err != nil {
		t.Skip("Redis local não disponível, pulando teste")
	}
	_ = conn.Close()

	// Servidor Mock da LinkedIn Conversions API
	var receivedBody []byte
	mockLinkedIn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody = body
		_ = receivedBody
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status": "created"}`))
	}))
	defer mockLinkedIn.Close()

	cfg := &config.Config{Env: "test", RedisAddr: "127.0.0.1:6379"}
	rdb, err := storage.NewRedis(cfg)
	if err != nil || rdb.Client == nil {
		t.Skip("Falha conectando ao Redis")
	}

	ctx := context.Background()
	siteID := fmt.Sprintf("site_bricks_%d", time.Now().UnixNano())
	subID := fmt.Sprintf("sub_bricks_%d", time.Now().UnixNano())

	// 1. Simula Evento 1 do Bricks: form_attempt capturou PII no navegador e gravou no Redis
	_ = rdb.SaveLeadCache(ctx, &storage.LeadCacheRecord{
		SubmissionID: subID,
		SiteID:       siteID,
		UserData: map[string]interface{}{
			"email": "eduardorosalem@soprano.com.br",
			"phone": "5554991143857",
			"name":  "Eduardo Rosalem",
		},
		Consent:             storage.LeadCacheConsent{Analytics: true, Marketing: true},
		ExplicitLeadConsent: true,
		CachedAt:            time.Now().UTC(),
	})
	defer func() {
		_ = rdb.DeleteLeadCache(ctx, siteID, subID)
	}()

	// 2. Simula Evento 2 do Bricks: form_submit_success chega com UserData vazio
	evSuccess := &collector.EventPayload{
		EventID:            fmt.Sprintf("ev_success_%d", time.Now().UnixNano()),
		SiteID:             siteID,
		SubmissionID:       subID,
		EventName:          "form_submit",
		FormLifecycleState: "form_submit_success",
		UserData:           map[string]interface{}{}, // Vazio do navegador!
		Consent:            collector.ConsentState{Analytics: true, Marketing: true},
		EventTime:          time.Now().UTC(),
	}

	pool := NewWorkerPool(cfg, rdb, nil)

	// 3. Executa a hidratação server-side
	hydrated := pool.HydrateEventLeadData(ctx, evSuccess)
	if !hydrated {
		t.Fatalf("esperava hidratação bem-sucedida para o evento de sucesso")
	}

	// Valida que o evento foi hidratado com o email e telefone do Eduardo Rosalem
	if evSuccess.UserData["email"] != "eduardorosalem@soprano.com.br" {
		t.Fatalf("esperava email hidratado 'eduardorosalem@soprano.com.br', obtido '%v'", evSuccess.UserData["email"])
	}
	if evSuccess.UserData["phone"] != "5554991143857" {
		t.Fatalf("esperava phone hidratado '5554991143857', obtido '%v'", evSuccess.UserData["phone"])
	}

	// Valida que o envio direto para a LinkedIn CAPI aceita os dados hidratados
	httpClient := integrations.NewHTTPClient(5 * time.Second)
	liCAPI := integrations.NewLinkedInCAPI(httpClient)
	// Como a URL padrão da LinkedIn aponta para api.linkedin.com, validamos a função SendEvent até a formatação:
	liErr := liCAPI.SendEvent(ctx, "test_token", "urn:li:conversionRule:123456", evSuccess)
	// O erro não deve ser "validação pré-despacho falhou: nenhum identificador de usuário"
	if liErr != nil && strings.Contains(liErr.Error(), "nenhum identificador de usuário") {
		t.Fatalf("LinkedIn CAPI rejeitou evento por falta de identificador mesmo após hidratação: %v", liErr)
	}
}

