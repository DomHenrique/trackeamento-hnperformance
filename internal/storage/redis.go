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

func (r *RedisClient) Close() error {
	if r.Client != nil {
		return r.Client.Close()
	}
	return nil
}
