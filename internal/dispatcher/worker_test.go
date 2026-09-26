package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"tracking-engine/internal/config"
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
