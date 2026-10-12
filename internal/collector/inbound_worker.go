package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"tracking-engine/internal/inbound"
	"tracking-engine/internal/storage"
)

const (
	InboundEventReadyChannel = "pubsub:inbound_event_ready"
	DefaultLeaseDuration     = 60 * time.Second
	DefaultPollInterval      = 3 * time.Second
	DefaultSweeperInterval   = 30 * time.Second
	DefaultWorkerBatchSize   = 20
)

// InboundWorker orquestra o consumo durável de webhooks de CRM/ERP do PostgreSQL via SKIP LOCKED
type InboundWorker struct {
	pg              *storage.PostgresDB
	redis           *storage.RedisClient
	reconciler      OfflineReconcileFunc
	notifyCh        chan struct{}
	stopCh          chan struct{}
	wg              sync.WaitGroup
	batchSize       int
	leaseDuration   time.Duration
	pollInterval    time.Duration
	sweeperInterval time.Duration
}

// NewInboundWorker instancia o worker assíncrono com tolerância a falhas e lease
func NewInboundWorker(pg *storage.PostgresDB, rdb *storage.RedisClient, reconciler OfflineReconcileFunc) *InboundWorker {
	return &InboundWorker{
		pg:              pg,
		redis:           rdb,
		reconciler:      reconciler,
		notifyCh:        make(chan struct{}, 100),
		stopCh:          make(chan struct{}),
		batchSize:       DefaultWorkerBatchSize,
		leaseDuration:   DefaultLeaseDuration,
		pollInterval:    DefaultPollInterval,
		sweeperInterval: DefaultSweeperInterval,
	}
}

// Notify acorda o worker imediatamente para processamento sem esperar pelo poll
func (w *InboundWorker) Notify() {
	select {
	case w.notifyCh <- struct{}{}:
	default:
	}
}

// Start inicia as goroutines do consumidor com lease e do sweeper de resgate
func (w *InboundWorker) Start(ctx context.Context) {
	if w.pg == nil || w.pg.Pool == nil {
		log.Println("[InboundWorker] Banco de dados indisponível, worker não iniciado")
		return
	}

	w.wg.Add(2)
	go w.runConsumerLoop(ctx)
	go w.runSweeperLoop(ctx)

	// Opcional: Subscriber do canal Redis Pub/Sub para acordar worker entre réplicas
	if w.redis != nil && w.redis.Client != nil {
		w.wg.Add(1)
		go w.runRedisSubscriber(ctx)
	}

	log.Println("[InboundWorker] Motor de reconciliação durável iniciado com sucesso (SKIP LOCKED + Lease Sweeper)")
}

// Stop finaliza graciosamente as rotinas em background
func (w *InboundWorker) Stop() {
	close(w.stopCh)
	w.wg.Wait()
	log.Println("[InboundWorker] Motor de reconciliação finalizado graciosamente")
}

func (w *InboundWorker) runConsumerLoop(ctx context.Context) {
	defer w.wg.Done()
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stopCh:
			return
		case <-ctx.Done():
			return
		case <-w.notifyCh:
			w.drainEligibleEvents(ctx)
		case <-ticker.C:
			w.drainEligibleEvents(ctx)
		}
	}
}

func (w *InboundWorker) drainEligibleEvents(ctx context.Context) {
	for {
		processed, err := w.processBatch(ctx)
		if err != nil {
			log.Printf("[InboundWorker] Erro ao processar lote: %v", err)
			break
		}
		if processed == 0 {
			break
		}
	}
}

type inboundJobRecord struct {
	ID          uuid.UUID
	SiteID      string
	Provider    string
	EventName   string
	Email       string
	Phone       string
	ValueCents  int64
	Currency    string
	OrderID     string
	RawPayload  []byte
	Attempts    int
	MaxAttempts int
	OccurredAt  time.Time
}

func (w *InboundWorker) processBatch(ctx context.Context) (int, error) {
	if w.pg == nil || w.pg.Pool == nil {
		return 0, nil
	}

	// 1. Inicia transação para reivindicar os eventos com FOR UPDATE SKIP LOCKED
	tx, err := w.pg.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		SELECT id, site_id::text, provider, event_name,
		       COALESCE(contact_email, ''), COALESCE(contact_phone, ''),
		       value_cents, currency, COALESCE(order_id, ''),
		       payload, attempts, max_attempts, occurred_at
		FROM crm_inbound_events
		WHERE status = 'pending' AND next_attempt_at <= now()
		ORDER BY created_at ASC
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, w.batchSize)
	if err != nil {
		return 0, err
	}

	var jobs []inboundJobRecord
	for rows.Next() {
		var job inboundJobRecord
		if errScan := rows.Scan(
			&job.ID, &job.SiteID, &job.Provider, &job.EventName,
			&job.Email, &job.Phone,
			&job.ValueCents, &job.Currency, &job.OrderID,
			&job.RawPayload, &job.Attempts, &job.MaxAttempts, &job.OccurredAt,
		); errScan == nil {
			jobs = append(jobs, job)
		}
	}
	rows.Close()

	if len(jobs) == 0 {
		return 0, nil
	}

	// 2. Marca atomicamente o lote reivindicado com status 'processing' e lease_expires_at
	leaseUntil := time.Now().Add(w.leaseDuration)
	for _, job := range jobs {
		_, errUpd := tx.Exec(ctx, `
			UPDATE crm_inbound_events
			SET status = 'processing',
			    locked_at = now(),
			    lease_expires_at = $1
			WHERE id = $2
		`, leaseUntil, job.ID)
		if errUpd != nil {
			log.Printf("[InboundWorker] Falha ao marcar lease para evento %s: %v", job.ID, errUpd)
		}
	}

	// Commit da posse do lease
	if errCommit := tx.Commit(ctx); errCommit != nil {
		return 0, errCommit
	}

	// 3. Executa a reconciliação de cada evento fora da transação de lock
	for _, job := range jobs {
		w.executeJob(ctx, job)
	}

	return len(jobs), nil
}

func (w *InboundWorker) executeJob(ctx context.Context, job inboundJobRecord) {
	var payloadMap map[string]interface{}
	if len(job.RawPayload) > 0 {
		_ = json.Unmarshal(job.RawPayload, &payloadMap)
	}

	norm := &inbound.NormalizedCRMEvent{
		Provider:   job.Provider,
		EventName:  job.EventName,
		Email:      job.Email,
		Phone:      job.Phone,
		Value:      float64(job.ValueCents) / 100.0,
		ValueCents: job.ValueCents,
		Currency:   job.Currency,
		OrderID:    job.OrderID,
		OccurredAt: job.OccurredAt,
		RawPayload: payloadMap,
	}

	var errReconcile error
	if w.reconciler != nil {
		jobCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		errReconcile = w.reconciler(jobCtx, job.SiteID, norm, job.ID.String())
		cancel()
	}

	if errReconcile == nil {
		// Sucesso: marca como 'done' e limpa locked_at
		_, _ = w.pg.Pool.Exec(ctx, `
			UPDATE crm_inbound_events
			SET status = 'done',
			    processed_at = now(),
			    locked_at = NULL,
			    error_message = NULL
			WHERE id = $1
		`, job.ID)
	} else {
		// Falha: incrementa attempts e programa retentativa com backoff exponencial ou marca como 'failed'
		newAttempts := job.Attempts + 1
		if newAttempts >= job.MaxAttempts {
			_, _ = w.pg.Pool.Exec(ctx, `
				UPDATE crm_inbound_events
				SET status = 'failed',
				    attempts = $1,
				    processed_at = now(),
				    locked_at = NULL,
				    error_message = $2
				WHERE id = $3
			`, newAttempts, fmt.Sprintf("esgotadas tentativas máximas: %v", errReconcile), job.ID)
			log.Printf("[InboundWorker] Evento %s marcado como FAILED após %d tentativas: %v", job.ID, newAttempts, errReconcile)
		} else {
			// Backoff exponencial: 10s, 30s, 90s...
			backoffSec := int(10 * (1 << (newAttempts - 1)))
			nextAttempt := time.Now().Add(time.Duration(backoffSec) * time.Second)
			_, _ = w.pg.Pool.Exec(ctx, `
				UPDATE crm_inbound_events
				SET status = 'pending',
				    attempts = $1,
				    next_attempt_at = $2,
				    locked_at = NULL,
				    error_message = $3
				WHERE id = $4
			`, newAttempts, nextAttempt, errReconcile.Error(), job.ID)
			log.Printf("[InboundWorker] Evento %s agendado para retentativa %d/%d em %ds: %v",
				job.ID, newAttempts, job.MaxAttempts, backoffSec, errReconcile)
		}
	}
}

func (w *InboundWorker) runSweeperLoop(ctx context.Context) {
	defer w.wg.Done()
	ticker := time.NewTicker(w.sweeperInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.recoverExpiredLeases(ctx)
		}
	}
}

func (w *InboundWorker) recoverExpiredLeases(ctx context.Context) {
	if w.pg == nil || w.pg.Pool == nil {
		return
	}

	// Resgata eventos órfãos onde status='processing' e lease_expires_at < now()
	cmdTag, err := w.pg.Pool.Exec(ctx, `
		UPDATE crm_inbound_events
		SET status = CASE
				WHEN attempts + 1 >= max_attempts THEN 'failed'
				ELSE 'pending'
		    END,
		    attempts = attempts + 1,
		    next_attempt_at = now() + interval '10 seconds',
		    locked_at = NULL,
		    error_message = 'lease expirado (resgatado pelo sweeper após timeout ou crash)'
		WHERE status = 'processing' AND lease_expires_at < now()
	`)
	if err == nil && cmdTag.RowsAffected() > 0 {
		log.Printf("[InboundWorker/Sweeper] Resgatados %d eventos órfãos com lease expirado", cmdTag.RowsAffected())
		w.Notify()
	}
}

func (w *InboundWorker) runRedisSubscriber(ctx context.Context) {
	defer w.wg.Done()
	pubsub := w.redis.Client.Subscribe(ctx, InboundEventReadyChannel)
	defer pubsub.Close()

	ch := pubsub.Channel()
	for {
		select {
		case <-w.stopCh:
			return
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if msg != nil {
				w.Notify()
			}
		}
	}
}
