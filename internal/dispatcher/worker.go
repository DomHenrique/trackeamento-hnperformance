package dispatcher

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
	"tracking-engine/internal/integrations"
	"tracking-engine/internal/storage"
)

type DispatchMessage struct {
	Event      *collector.EventPayload `json:"event"`
	FirstTouch map[string]interface{}  `json:"first_touch"`
	QueuedAt   time.Time               `json:"queued_at"`
}

type WorkerPool struct {
	cfg        *config.Config
	redis      *storage.RedisClient
	pg         *storage.PostgresDB
	httpClient *integrations.HTTPClient

	metaCAPI  *integrations.MetaCAPI
	ga4MP     *integrations.GA4MP
	googleAds *integrations.GoogleAds
	crm       *integrations.CRMWebhook
}

func NewWorkerPool(cfg *config.Config, rdb *storage.RedisClient, pg *storage.PostgresDB) *WorkerPool {
	client := integrations.NewHTTPClient(time.Duration(cfg.DispatcherTimeoutSec) * time.Second)

	return &WorkerPool{
		cfg:        cfg,
		redis:      rdb,
		pg:         pg,
		httpClient: client,
		metaCAPI:   integrations.NewMetaCAPI(client),
		ga4MP:      integrations.NewGA4MP(client),
		googleAds:  integrations.NewGoogleAds(client),
		crm:        integrations.NewCRMWebhook(client),
	}
}

// Start inicia o pool de goroutines e o loop de leitura da fila de despacho
func (wp *WorkerPool) Start(ctx context.Context) error {
	stream := wp.cfg.RedisStreamDispatch
	group := wp.cfg.RedisConsumerGroup + "_dispatch"

	_ = wp.redis.EnsureConsumerGroup(ctx, stream, group)

	numWorkers := wp.cfg.DispatcherWorkers
	if numWorkers <= 0 {
		numWorkers = 4
	}

	log.Printf("==> Dispatcher iniciado com %d workers concorrentes. Escutando '%s'...", numWorkers, stream)

	jobs := make(chan redis.XMessage, 100)
	var wg sync.WaitGroup

	// Inicia os workers do pool
	for i := 1; i <= numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case msg, ok := <-jobs:
					if !ok {
						return
					}
					wp.processMessage(ctx, stream, group, msg)
				}
			}
		}(i)
	}

	consumerName := fmt.Sprintf("disp-consumer-%d", time.Now().Unix())

	// Loop principal distribuidor
	for {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return nil
		default:
			streams, err := wp.redis.Client.XReadGroup(ctx, &redis.XReadGroupArgs{
				Group:    group,
				Consumer: consumerName,
				Streams:  []string{stream, ">"},
				Count:    int64(numWorkers * 2),
				Block:    2 * time.Second,
			}).Result()

			if err != nil {
				if err != redis.Nil && ctx.Err() == nil {
					time.Sleep(500 * time.Millisecond)
				}
				continue
			}

			for _, s := range streams {
				for _, msg := range s.Messages {
					jobs <- msg
				}
			}
		}
	}
}

func (wp *WorkerPool) processMessage(ctx context.Context, stream, group string, msg redis.XMessage) {
	payloadRaw, ok := msg.Values["payload"].(string)
	if !ok {
		if b, ok2 := msg.Values["payload"].([]byte); ok2 {
			payloadRaw = string(b)
		}
	}

	var dMsg DispatchMessage
	if err := json.Unmarshal([]byte(payloadRaw), &dMsg); err != nil {
		_ = wp.redis.Client.XAck(ctx, stream, group, msg.ID).Err()
		return
	}

	ev := dMsg.Event
	if ev == nil {
		_ = wp.redis.Client.XAck(ctx, stream, group, msg.ID).Err()
		return
	}

	// Busca integrações ativas do site no PostgreSQL
	integrationsList := wp.loadSiteIntegrations(ctx, ev.SiteID)

	var dispatchWG sync.WaitGroup

	for _, integ := range integrationsList {
		dispatchWG.Add(1)
		go func(it SiteIntegrationRecord) {
			defer dispatchWG.Done()
			callCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			switch it.Platform {
			case "meta_capi":
				pixelID := it.Credentials["pixel_id"]
				token := it.Credentials["access_token"]
				testCode := it.Credentials["test_event_code"]
				if err := wp.metaCAPI.SendEvent(callCtx, pixelID, token, testCode, ev); err != nil {
					log.Printf("[Dispatcher] Falha Meta CAPI (site %s): %v", ev.SiteID, err)
				} else {
					log.Printf("[Dispatcher] SUCESSO Meta CAPI: evento '%s' enviado!", ev.EventName)
				}

			case "ga4":
				measurementID := it.Credentials["measurement_id"]
				apiSecret := it.Credentials["api_secret"]
				if err := wp.ga4MP.SendEvent(callCtx, measurementID, apiSecret, ev); err != nil {
					log.Printf("[Dispatcher] Falha GA4 MP (site %s): %v", ev.SiteID, err)
				} else {
					log.Printf("[Dispatcher] SUCESSO GA4 MP: evento '%s' enviado!", ev.EventName)
				}

			case "google_ads":
				endpointURL := it.Credentials["endpoint_url"]
				token := it.Credentials["api_token"]
				if err := wp.googleAds.SendConversion(callCtx, endpointURL, token, ev); err != nil {
					log.Printf("[Dispatcher] Falha Google Ads (site %s): %v", ev.SiteID, err)
				}

			case "webhook":
				url := it.Credentials["webhook_url"]
				secret := it.Credentials["secret_token"]
				if err := wp.crm.SendWebhook(callCtx, url, secret, ev, dMsg.FirstTouch); err != nil {
					log.Printf("[Dispatcher] Falha Webhook CRM (site %s): %v", ev.SiteID, err)
				} else {
					log.Printf("[Dispatcher] SUCESSO Webhook CRM: lead despachado para %s!", url)
				}
			}
		}(integ)
	}

	dispatchWG.Wait()

	// Confirma ACK no Redis após todas as integrações serem acionadas
	_ = wp.redis.Client.XAck(ctx, stream, group, msg.ID).Err()
}

type SiteIntegrationRecord struct {
	Platform    string
	Credentials map[string]string
}

func (wp *WorkerPool) loadSiteIntegrations(ctx context.Context, siteIDStr string) []SiteIntegrationRecord {
	var results []SiteIntegrationRecord

	siteUUID, err := uuid.Parse(siteIDStr)
	if err != nil || wp.pg == nil || wp.pg.Pool == nil {
		return results
	}

	rows, err := wp.pg.Pool.Query(ctx, `
		SELECT platform, credentials 
		FROM site_integrations 
		WHERE site_id = $1 AND is_active = true
	`, siteUUID)
	if err != nil {
		return results
	}
	defer rows.Close()

	for rows.Next() {
		var platform string
		var credsJSON []byte
		if err := rows.Scan(&platform, &credsJSON); err == nil {
			var credsMap map[string]string
			_ = json.Unmarshal(credsJSON, &credsMap)
			results = append(results, SiteIntegrationRecord{
				Platform:    platform,
				Credentials: credsMap,
			})
		}
	}

	return results
}
