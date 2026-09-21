package storage

import (
	"context"
	"fmt"
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
