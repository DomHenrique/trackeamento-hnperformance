package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"tracking-engine/internal/config"
)

type RedisClient struct {
	Client *redis.Client
	cfg    *config.Config
}

func NewRedis(cfg *config.Config) (*RedisClient, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rdb := redis.NewClient(&redis.Options{
		Addr:         cfg.RedisAddr,
		Password:     cfg.RedisPassword,
		DB:           0,
		PoolSize:     50,
		MinIdleConns: 5,
		DialTimeout:  3 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	})

	if err := rdb.Ping(ctx).Err(); err != nil {
		fmt.Printf("Aviso: ping inicial no Redis falhou (%v). O serviço continuará tentando.\n", err)
	}

	return &RedisClient{
		Client: rdb,
		cfg:    cfg,
	}, nil
}

// PushRawEvent insere o evento bruto em formato JSON no Redis Stream (limitando tamanho para prevenir exaustão de memória)
func (r *RedisClient) PushRawEvent(ctx context.Context, eventJSON []byte) error {
	return r.Client.XAdd(ctx, &redis.XAddArgs{
		Stream: r.cfg.RedisStreamRaw,
		MaxLen: 200000,
		Approx: true,
		Values: map[string]interface{}{
			"payload": eventJSON,
		},
	}).Err()
}

// PushDispatchEvent enfileira eventos qualificados para o Dispatcher (Meta, Google, CRM)
func (r *RedisClient) PushDispatchEvent(ctx context.Context, dispatchJSON []byte) error {
	return r.Client.XAdd(ctx, &redis.XAddArgs{
		Stream: r.cfg.RedisStreamDispatch,
		MaxLen: 200000,
		Approx: true,
		Values: map[string]interface{}{
			"payload": dispatchJSON,
		},
	}).Err()
}

// EnsureConsumerGroup garante que o grupo consumidor do Redis Stream existe
func (r *RedisClient) EnsureConsumerGroup(ctx context.Context, stream, group string) error {
	err := r.Client.XGroupCreateMkStream(ctx, stream, group, "$").Err()
	if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		return err
	}
	return nil
}

// PublishDebugEvent transmite o evento ao vivo via Redis Pub/Sub para clientes SSE conectados
func (r *RedisClient) PublishDebugEvent(ctx context.Context, siteID string, eventJSON []byte) error {
	channel := fmt.Sprintf("debug:stream:%s", siteID)
	return r.Client.Publish(ctx, channel, eventJSON).Err()
}

// PushDebugBuffer guarda o evento no buffer circular volátil (últimos 100 itens com TTL de 1h)
func (r *RedisClient) PushDebugBuffer(ctx context.Context, siteID string, eventJSON []byte) error {
	key := fmt.Sprintf("debug:events:%s", siteID)
	pipe := r.Client.Pipeline()
	pipe.LPush(ctx, key, eventJSON)
	pipe.LTrim(ctx, key, 0, 99)
	pipe.Expire(ctx, key, time.Hour)
	_, err := pipe.Exec(ctx)
	return err
}

// GetRecentDebugEvents retorna os eventos recentes do buffer circular para inicializar o DebugView
func (r *RedisClient) GetRecentDebugEvents(ctx context.Context, siteID string, limit int64) ([]string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	key := fmt.Sprintf("debug:events:%s", siteID)
	return r.Client.LRange(ctx, key, 0, limit-1).Result()
}

// SubscribeDebug cria uma subscrição Pub/Sub para o canal de debug do site
func (r *RedisClient) SubscribeDebug(ctx context.Context, siteID string) *redis.PubSub {
	channel := fmt.Sprintf("debug:stream:%s", siteID)
	return r.Client.Subscribe(ctx, channel)
}

func (r *RedisClient) Close() error {
	if r.Client != nil {
		return r.Client.Close()
	}
	return nil
}

// AcquireDispatchLease tenta obter um lease para processar o envio de um evento para uma integração específica.
// Retorna acquired=true se o worker obteve permissão de envio.
// Retorna alreadySucceeded=true se o evento já foi despachado com sucesso anteriormente (idempotência).
func (r *RedisClient) AcquireDispatchLease(ctx context.Context, integrationID, eventID string, leaseDuration time.Duration) (acquired bool, alreadySucceeded bool, attempts int, err error) {
	if r.Client == nil {
		return true, false, 1, nil
	}

	key := fmt.Sprintf("dispatch:lock:%s:%s", integrationID, eventID)
	now := time.Now().UTC()
	nowMs := now.UnixMilli()

	vals, err := r.Client.HGetAll(ctx, key).Result()
	if err != nil && err != redis.Nil {
		return false, false, 0, err
	}

	parseMs := func(val string) int64 {
		v, _ := strconv.ParseInt(val, 10, 64)
		if v > 0 && v < 100000000000 { // compatibilidade com timestamps legados em segundos
			return v * 1000
		}
		return v
	}

	if len(vals) == 0 {
		pipe := r.Client.TxPipeline()
		pipe.HSet(ctx, key, map[string]interface{}{
			"status":           "processing",
			"lease_expires_at": now.Add(leaseDuration).UnixMilli(),
			"next_retry_at":    0,
			"attempts":         1,
		})
		pipe.Expire(ctx, key, 7*24*time.Hour)
		_, err = pipe.Exec(ctx)
		if err != nil {
			return false, false, 0, err
		}
		return true, false, 1, nil
	}

	status := vals["status"]
	attempts, _ = strconv.Atoi(vals["attempts"])
	if attempts <= 0 {
		attempts = 1
	}

	if status == "succeeded" {
		return false, true, attempts, nil
	}
	if status == "permanent_failure" {
		return false, true, attempts, nil
	}

	if status == "processing" {
		leaseExpMs := parseMs(vals["lease_expires_at"])
		if leaseExpMs > nowMs {
			return false, false, attempts, nil
		}
		attempts++
		r.Client.HSet(ctx, key, map[string]interface{}{
			"status":           "processing",
			"lease_expires_at": now.Add(leaseDuration).UnixMilli(),
			"attempts":         attempts,
		})
		return true, false, attempts, nil
	}

	if status == "retryable_failure" {
		nextRetryMs := parseMs(vals["next_retry_at"])
		if nextRetryMs > nowMs {
			return false, false, attempts, nil
		}
		attempts++
		r.Client.HSet(ctx, key, map[string]interface{}{
			"status":           "processing",
			"lease_expires_at": now.Add(leaseDuration).UnixMilli(),
			"attempts":         attempts,
		})
		return true, false, attempts, nil
	}

	return false, false, attempts, nil
}

// MarkDispatchSuccess registra que o evento foi despachado com sucesso definitivo para a integração
func (r *RedisClient) MarkDispatchSuccess(ctx context.Context, integrationID, eventID string) error {
	if r.Client == nil {
		return nil
	}
	key := fmt.Sprintf("dispatch:lock:%s:%s", integrationID, eventID)
	pipe := r.Client.TxPipeline()
	pipe.HSet(ctx, key, map[string]interface{}{
		"status":       "succeeded",
		"completed_at": time.Now().UTC().UnixMilli(),
	})
	pipe.Expire(ctx, key, 7*24*time.Hour)
	_, err := pipe.Exec(ctx)
	return err
}

// MarkDispatchRetry registra que houve falha temporária e agenda o próximo horário de tentativa
func (r *RedisClient) MarkDispatchRetry(ctx context.Context, integrationID, eventID string, attempts int, backoffDelay time.Duration, lastErr string) error {
	if r.Client == nil {
		return nil
	}
	key := fmt.Sprintf("dispatch:lock:%s:%s", integrationID, eventID)
	pipe := r.Client.TxPipeline()
	pipe.HSet(ctx, key, map[string]interface{}{
		"status":        "retryable_failure",
		"attempts":      attempts,
		"last_error":    lastErr,
		"next_retry_at": time.Now().UTC().Add(backoffDelay).UnixMilli(),
	})
	pipe.Expire(ctx, key, 7*24*time.Hour)
	_, err := pipe.Exec(ctx)
	return err
}

// MarkDispatchPermanentFailure registra erro terminal impedindo novas tentativas
func (r *RedisClient) MarkDispatchPermanentFailure(ctx context.Context, integrationID, eventID string, attempts int, lastErr string) error {
	if r.Client == nil {
		return nil
	}
	key := fmt.Sprintf("dispatch:lock:%s:%s", integrationID, eventID)
	pipe := r.Client.TxPipeline()
	pipe.HSet(ctx, key, map[string]interface{}{
		"status":     "permanent_failure",
		"attempts":   attempts,
		"last_error": lastErr,
		"failed_at":  time.Now().UTC().Unix(),
	})
	pipe.Expire(ctx, key, 7*24*time.Hour)
	_, err := pipe.Exec(ctx)
	return err
}

// PushDeadLetter enfileira mensagem na Dead-Letter Queue com metadados detalhados de diagnóstico
func (r *RedisClient) PushDeadLetter(ctx context.Context, stream string, data map[string]interface{}) error {
	if r.Client == nil {
		return nil
	}
	if stream == "" {
		stream = "stream:events:dispatch:dead_letter"
	}
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return r.Client.XAdd(ctx, &redis.XAddArgs{
		Stream: stream,
		MaxLen: 50000,
		Approx: true,
		Values: map[string]interface{}{
			"payload": b,
		},
	}).Err()
}

// AutoClaimPending reivindica mensagens pendentes no PEL há mais tempo que minIdle
func (r *RedisClient) AutoClaimPending(ctx context.Context, stream, group, consumer string, minIdle time.Duration, start string, count int64) ([]redis.XMessage, string, error) {
	if r.Client == nil {
		return nil, "0-0", nil
	}
	if count <= 0 {
		count = 50
	}
	if start == "" {
		start = "0-0"
	}
	res, nextStart, err := r.Client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream:   stream,
		Group:    group,
		Consumer: consumer,
		MinIdle:  minIdle,
		Start:    start,
		Count:    count,
	}).Result()
	return res, nextStart, err
}
