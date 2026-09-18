package ingester

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"tracking-engine/internal/collector"
	"tracking-engine/internal/config"
	"tracking-engine/internal/identity"
	"tracking-engine/internal/storage"
)

type Worker struct {
	cfg      *config.Config
	redis    *storage.RedisClient
	ch       *storage.ClickHouseDB
	identity *identity.Service

	mu          sync.Mutex
	batch       []*collector.EventPayload
	msgIDs      []string
	lastFlushAt time.Time
}

func NewWorker(cfg *config.Config, rdb *storage.RedisClient, ch *storage.ClickHouseDB, ident *identity.Service) *Worker {
	return &Worker{
		cfg:         cfg,
		redis:       rdb,
		ch:          ch,
		identity:    ident,
		batch:       make([]*collector.EventPayload, 0, cfg.IngesterBatchSize),
		msgIDs:      make([]string, 0, cfg.IngesterBatchSize),
		lastFlushAt: time.Now(),
	}
}

// Start inicia o loop contínuo de consumo do Redis e gravação em batch no ClickHouse
func (w *Worker) Start(ctx context.Context) error {
	stream := w.cfg.RedisStreamRaw
	group := w.cfg.RedisConsumerGroup
	consumer := fmt.Sprintf("ingester-%d", time.Now().Unix())

	// Garante que o consumer group existe
	_ = w.redis.EnsureConsumerGroup(ctx, stream, group)

	log.Printf("==> Ingester iniciado. Escutando stream '%s' (Grupo: %s)...", stream, group)

	ticker := time.NewTicker(time.Duration(w.cfg.IngesterFlushSec) * time.Second)
	defer ticker.Stop()

	// Goroutine para o timer de flush por tempo
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.mu.Lock()
				if len(w.batch) > 0 {
					log.Printf("[Ingester] Trigger de tempo acionado (%d eventos). Executando flush...", len(w.batch))
					w.flushLocked(context.Background())
				}
				w.mu.Unlock()
			}
		}
	}()

	// Loop principal de leitura do Redis
	for {
		select {
		case <-ctx.Done():
			w.mu.Lock()
			if len(w.batch) > 0 {
				w.flushLocked(context.Background())
			}
			w.mu.Unlock()
			return nil
		default:
			streams, err := w.redis.Client.XReadGroup(ctx, &redis.XReadGroupArgs{
				Group:    group,
				Consumer: consumer,
				Streams:  []string{stream, ">"},
				Count:    500,
				Block:    1 * time.Second,
			}).Result()

			if err != nil {
				if err != redis.Nil && ctx.Err() == nil {
					time.Sleep(500 * time.Millisecond)
				}
				continue
			}

			for _, s := range streams {
				for _, msg := range s.Messages {
					payloadRaw, ok := msg.Values["payload"].(string)
					if !ok {
						if b, ok2 := msg.Values["payload"].([]byte); ok2 {
							payloadRaw = string(b)
						}
					}

					var ev collector.EventPayload
					if err := json.Unmarshal([]byte(payloadRaw), &ev); err != nil {
						// Ignora payload corrompido e confirma mensagem
						_ = w.redis.Client.XAck(ctx, stream, group, msg.ID).Err()
						continue
					}

					w.mu.Lock()
					w.batch = append(w.batch, &ev)
					w.msgIDs = append(w.msgIDs, msg.ID)

					if w.identity != nil {
						go func(evItem collector.EventPayload) {
							identCtx, identCancel := context.WithTimeout(context.Background(), 5*time.Second)
							defer identCancel()
							_ = w.identity.ReconcileAndRoute(identCtx, &evItem)
						}(ev)
					}

					if len(w.batch) >= w.cfg.IngesterBatchSize {
						log.Printf("[Ingester] Limite de lote atingido (%d eventos). Executando flush...", len(w.batch))
						w.flushLocked(ctx)
					}
					w.mu.Unlock()
				}
			}
		}
	}
}

// flushLocked executa a inserção em lote no ClickHouse (DEVE ser chamado sob w.mu.Lock())
func (w *Worker) flushLocked(ctx context.Context) {
	if len(w.batch) == 0 {
		return
	}

	eventsToInsert := w.batch
	idsToAck := w.msgIDs

	// Reseta os buffers imediatamente para liberar novas mensagens
	w.batch = make([]*collector.EventPayload, 0, w.cfg.IngesterBatchSize)
	w.msgIDs = make([]string, 0, w.cfg.IngesterBatchSize)
	w.lastFlushAt = time.Now()

	go func(events []*collector.EventPayload, msgIDs []string) {
		flushCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		err := w.insertBatchClickHouse(flushCtx, events)
		if err != nil {
			log.Printf("[Ingester] ERRO ao inserir batch de %d eventos no ClickHouse: %v", len(events), err)
			// Não confirma ACK no Redis para que as mensagens permaneçam na fila
			return
		}

		// Confirmação de ACK em lote no Redis
		if len(msgIDs) > 0 {
			_ = w.redis.Client.XAck(flushCtx, w.cfg.RedisStreamRaw, w.cfg.RedisConsumerGroup, msgIDs...).Err()
		}
		log.Printf("[Ingester] SUCESSO: %d eventos persistidos no ClickHouse com ACK!", len(events))
	}(eventsToInsert, idsToAck)
}

func (w *Worker) insertBatchClickHouse(ctx context.Context, events []*collector.EventPayload) error {
	if w.ch == nil || w.ch.Conn == nil {
		return fmt.Errorf("conexao com ClickHouse indisponivel")
	}

	batch, err := w.ch.Conn.PrepareBatch(ctx, `
		INSERT INTO tracking_events.events (
			event_id, site_id, visitor_id, session_id, event_name, event_time,
			landing_page, page_url, referrer,
			utm_source, utm_medium, utm_campaign, utm_content, utm_term,
			gclid, gbraid, wbraid, fbclid, ttclid,
			ip_address, user_agent, device_type,
			custom_data_json, is_bot, bot_reason, created_at
		)
	`)
	if err != nil {
		return fmt.Errorf("erro no PrepareBatch ClickHouse: %w", err)
	}

	for _, ev := range events {
		eventUUID, _ := uuid.Parse(ev.EventID)
		siteUUID, _ := uuid.Parse(ev.SiteID)
		visitorUUID, _ := uuid.Parse(strings.TrimPrefix(ev.VisitorID, "v_"))
		sessionUUID, _ := uuid.Parse(strings.TrimPrefix(ev.SessionID, "s_"))

		customJSON, _ := json.Marshal(ev.CustomData)

		var isBotVal uint8
		if ev.IsBot {
			isBotVal = 1
		}

		err := batch.Append(
			eventUUID,
			siteUUID,
			visitorUUID,
			sessionUUID,
			ev.EventName,
			ev.EventTime,
			ev.Attribution.LandingPage,
			ev.Attribution.PageURL,
			ev.Attribution.Referrer,
			ev.Attribution.UTMSource,
			ev.Attribution.UTMMedium,
			ev.Attribution.UTMCampaign,
			ev.Attribution.UTMContent,
			ev.Attribution.UTMTerm,
			ev.Attribution.GCLID,
			ev.Attribution.GBRAID,
			ev.Attribution.WBRAID,
			ev.Attribution.FBCLID,
			ev.Attribution.TTCLID,
			ev.IPAddress,
			ev.UserAgent,
			ev.DeviceType,
			string(customJSON),
			isBotVal,
			ev.BotReason,
			ev.CreatedAt,
		)
		if err != nil {
			log.Printf("Aviso: falha ao anexar evento no batch ClickHouse: %v", err)
		}
	}

	return batch.Send()
}
