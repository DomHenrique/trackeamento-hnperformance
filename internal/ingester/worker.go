package ingester

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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

	log.Printf("==> Ingester iniciado. Escutando stream '%s' (Grupo: %s, Consumidor: %s)...", stream, group, consumer)

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

	// Goroutine periódica de XAutoClaim para recuperar mensagens órfãs no PEL há mais de 60s
	go func() {
		claimTicker := time.NewTicker(30 * time.Second)
		defer claimTicker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-claimTicker.C:
				claimedMsgs, _, err := w.redis.AutoClaimPending(ctx, stream, group, consumer, 60*time.Second, "0-0", 100)
				if err != nil {
					if err != redis.Nil && ctx.Err() == nil {
						log.Printf("[Ingester] Erro no XAutoClaim de eventos pendentes: %v", err)
					}
					continue
				}
				if len(claimedMsgs) > 0 {
					log.Printf("[Ingester] XAutoClaim reivindicou %d eventos pendentes do PEL.", len(claimedMsgs))
					w.mu.Lock()
					for _, msg := range claimedMsgs {
						payloadRaw, ok := msg.Values["payload"].(string)
						if !ok {
							if b, ok2 := msg.Values["payload"].([]byte); ok2 {
								payloadRaw = string(b)
							}
						}

						var ev collector.EventPayload
						if err := json.Unmarshal([]byte(payloadRaw), &ev); err != nil {
							_ = w.redis.Client.XAck(ctx, stream, group, msg.ID).Err()
							continue
						}

						w.batch = append(w.batch, &ev)
						w.msgIDs = append(w.msgIDs, msg.ID)
					}

					if len(w.batch) >= w.cfg.IngesterBatchSize {
						log.Printf("[Ingester] Batch atingiu limite (%d) após XAutoClaim. Executando flush...", len(w.batch))
						w.flushLocked(context.Background())
					}
					w.mu.Unlock()
				}
			}
		}
	}()

	// Loop principal de leitura do Redis
	for {
		select {
		case <-ctx.Done():
			w.mu.Lock()
			if len(w.batch) > 0 {
				log.Printf("[Ingester] Desligamento gracioso: persistindo %d eventos pendentes no ClickHouse...", len(w.batch))
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				w.flushLocked(shutdownCtx)
				cancel()
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
						// Ignora payload corrompido e confirma mensagem para não travar a fila
						_ = w.redis.Client.XAck(ctx, stream, group, msg.ID).Err()
						continue
					}

					w.mu.Lock()
					w.batch = append(w.batch, &ev)
					w.msgIDs = append(w.msgIDs, msg.ID)

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

// flushLocked executa a inserção em lote com sequenciamento estrito (ClickHouse -> Identity/Dispatch -> Redis XACK)
// IMPORTANTE: Deve ser chamado sob w.mu.Lock()
func (w *Worker) flushLocked(ctx context.Context) {
	if len(w.batch) == 0 {
		return
	}

	eventsToInsert := w.batch
	idsToAck := w.msgIDs

	// Reseta os buffers imediatamente
	w.batch = make([]*collector.EventPayload, 0, w.cfg.IngesterBatchSize)
	w.msgIDs = make([]string, 0, w.cfg.IngesterBatchSize)
	w.lastFlushAt = time.Now()

	flushCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// 1. Grava lote no ClickHouse de forma síncrona
	err := w.insertBatchClickHouse(flushCtx, eventsToInsert)
	if err != nil {
		log.Printf("[Ingester] ERRO ao inserir batch de %d eventos no ClickHouse: %v", len(eventsToInsert), err)
		// NÃO confirma ACK no Redis para que as mensagens permaneçam na fila para retry/reclaim
		return
	}

	// 2. Com a persistência analítica garantida no ClickHouse, reconcilia identidade e gera tarefas de despacho
	if w.identity != nil {
		for _, ev := range eventsToInsert {
			identCtx, identCancel := context.WithTimeout(flushCtx, 3*time.Second)
			_ = w.identity.ReconcileAndRoute(identCtx, ev)
			identCancel()
		}
	}

	// 3. Somente após ClickHouse gravado e despachos gerados, confirma XACK em lote no Redis
	if len(idsToAck) > 0 {
		_ = w.redis.Client.XAck(flushCtx, w.cfg.RedisStreamRaw, w.cfg.RedisConsumerGroup, idsToAck...).Err()
	}

	log.Printf("[Ingester] SUCESSO: %d eventos persistidos no ClickHouse e confirmados com XACK!", len(eventsToInsert))
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
		eventUUID, errEv := uuid.Parse(ev.EventID)
		if errEv != nil {
			eventUUID = uuid.NewSHA1(uuid.NameSpaceDNS, []byte(ev.EventID))
		}
		siteUUID, _ := uuid.Parse(ev.SiteID)
		visitorUUID, errVis := uuid.Parse(ev.VisitorID)
		if errVis != nil {
			visitorUUID = uuid.NewSHA1(uuid.NameSpaceDNS, []byte(ev.VisitorID))
		}
		sessionUUID, errSess := uuid.Parse(ev.SessionID)
		if errSess != nil {
			sessionUUID = uuid.NewSHA1(uuid.NameSpaceDNS, []byte(ev.SessionID))
		}

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
