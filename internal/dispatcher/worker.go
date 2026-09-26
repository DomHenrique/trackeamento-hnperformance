package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net"
	"strings"
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

	metaCAPI     *integrations.MetaCAPI
	ga4MP        *integrations.GA4MP
	googleAds    *integrations.GoogleAds
	linkedinCAPI *integrations.LinkedInCAPI
	crm          *integrations.CRMWebhook
}

func NewWorkerPool(cfg *config.Config, rdb *storage.RedisClient, pg *storage.PostgresDB) *WorkerPool {
	client := integrations.NewHTTPClient(time.Duration(cfg.DispatcherTimeoutSec) * time.Second)

	return &WorkerPool{
		cfg:          cfg,
		redis:        rdb,
		pg:           pg,
		httpClient:   client,
		metaCAPI:     integrations.NewMetaCAPI(client),
		ga4MP:        integrations.NewGA4MP(client),
		googleAds:    integrations.NewGoogleAds(client),
		linkedinCAPI: integrations.NewLinkedInCAPI(client),
		crm:          integrations.NewCRMWebhook(client),
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

	// Goroutine periódica de XAutoClaim para recuperar mensagens pendentes no PEL há mais de 60s
	go func() {
		claimTicker := time.NewTicker(30 * time.Second)
		defer claimTicker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-claimTicker.C:
				claimedMsgs, _, err := wp.redis.AutoClaimPending(ctx, stream, group, consumerName, 60*time.Second, "0-0", 50)
				if err != nil {
					if err != redis.Nil && ctx.Err() == nil {
						log.Printf("[Dispatcher] Erro no XAutoClaim de mensagens pendentes: %v", err)
					}
					continue
				}
				if len(claimedMsgs) > 0 {
					log.Printf("[Dispatcher] XAutoClaim reivindicou %d mensagens pendentes do PEL para reprocessamento.", len(claimedMsgs))
					for _, msg := range claimedMsgs {
						select {
						case jobs <- msg:
						case <-ctx.Done():
							return
						}
					}
				}
			}
		}
	}()

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

	// Gatekeeper Final de Privacidade: mensagens sem consentimento de marketing ou sob GPC são descartadas e confirmadas
	if !ev.Consent.Marketing || ev.PrivacySignals.GPC {
		log.Printf("[Dispatcher] Interceptado por compliance: evento %s site=%s sem consentimento de marketing (marketing=%v, gpc=%v). Despacho externo abortado.", ev.EventID, ev.SiteID, ev.Consent.Marketing, ev.PrivacySignals.GPC)
		_ = wp.redis.Client.XAck(ctx, stream, group, msg.ID).Err()
		return
	}

	// Busca integrações ativas do site no PostgreSQL
	integrationsList := wp.loadSiteIntegrations(ctx, ev.SiteID)
	if len(integrationsList) == 0 {
		_ = wp.redis.Client.XAck(ctx, stream, group, msg.ID).Err()
		return
	}

	var dispatchWG sync.WaitGroup
	var mu sync.Mutex
	allSettled := true

	for _, integ := range integrationsList {
		dispatchWG.Add(1)
		go func(it SiteIntegrationRecord) {
			defer dispatchWG.Done()

			integID := it.ID
			if integID == "" {
				integID = fmt.Sprintf("%s_%s", ev.SiteID, it.Platform)
			}

			// 1. Tenta adquirir lease de processamento (TTL de 45 segundos)
			acquired, alreadySucceeded, attempts, err := wp.redis.AcquireDispatchLease(ctx, integID, ev.EventID, 45*time.Second)
			if err != nil {
				log.Printf("[Dispatcher] Erro ao adquirir lease de despacho (%s / %s): %v", integID, ev.EventID, err)
				mu.Lock()
				allSettled = false
				mu.Unlock()
				return
			}

			if alreadySucceeded {
				log.Printf("[Dispatcher] Evento %s já foi despachado com sucesso anteriormente para integração %s. Ignorando reenvio.", ev.EventID, integID)
				return
			}

			if !acquired {
				// Outro worker está processando ou ainda em janela de backoff
				log.Printf("[Dispatcher] Lease não adquirida para evento %s na integração %s (worker ativo ou aguardando backoff).", ev.EventID, integID)
				mu.Lock()
				allSettled = false
				mu.Unlock()
				return
			}

			callCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			var callErr error
			switch it.Platform {
			case "meta_capi":
				pixelID := it.Credentials["pixel_id"]
				token := it.Credentials["access_token"]
				testCode := it.Credentials["test_event_code"]
				callErr = wp.metaCAPI.SendEvent(callCtx, pixelID, token, testCode, ev)

			case "ga4":
				measurementID := it.Credentials["measurement_id"]
				apiSecret := it.Credentials["api_secret"]
				callErr = wp.ga4MP.SendEvent(callCtx, measurementID, apiSecret, ev)

			case "google_ads":
				endpointURL := it.Credentials["endpoint_url"]
				token := it.Credentials["api_token"]
				callErr = wp.googleAds.SendConversion(callCtx, endpointURL, token, ev)

			case "linkedin_capi":
				token := it.Credentials["access_token"]
				ruleID := it.Credentials["conversion_rule_id"]
				callErr = wp.linkedinCAPI.SendEvent(callCtx, token, ruleID, ev)

			case "webhook":
				url := it.Credentials["webhook_url"]
				secret := it.Credentials["secret_token"]
				callErr = wp.crm.SendWebhook(callCtx, url, secret, ev, dMsg.FirstTouch)
			}

			if callErr == nil {
				log.Printf("[Dispatcher] SUCESSO %s: evento '%s' (%s) enviado com êxito!", it.Platform, ev.EventName, ev.EventID)
				_ = wp.redis.MarkDispatchSuccess(ctx, integID, ev.EventID)
				return
			}

			// Houve erro no disparo
			log.Printf("[Dispatcher] Falha no disparo %s (site %s, evento %s, tentativa %d): %v", it.Platform, ev.SiteID, ev.EventID, attempts, callErr)

			isPermanent := isPermanentError(callErr)
			if isPermanent || attempts >= 5 {
				// Erro terminal ou esgotamento de tentativas -> mover para Dead-Letter Queue
				log.Printf("[Dispatcher] Encaminhando evento %s para Dead-Letter Queue (tentativas=%d, permanente=%v, erro=%v)", ev.EventID, attempts, isPermanent, callErr)
				_ = wp.redis.MarkDispatchPermanentFailure(ctx, integID, ev.EventID, attempts, callErr.Error())
				dlqPayload := map[string]interface{}{
					"integration_id":     integID,
					"platform":           it.Platform,
					"event_id":           ev.EventID,
					"event_name":         ev.EventName,
					"site_id":            ev.SiteID,
					"attempts":           attempts,
					"is_permanent_error": isPermanent,
					"last_error_message": callErr.Error(),
					"last_failed_at":     time.Now().UTC().Format(time.RFC3339),
					"event":              ev,
					"first_touch":        dMsg.FirstTouch,
				}
				_ = wp.redis.PushDeadLetter(ctx, "stream:events:dispatch:dead_letter", dlqPayload)
			} else {
				// Erro transitório (< 5 tentativas) -> agendar retry com backoff exponencial e jitter
				backoff := calculateBackoff(attempts)
				log.Printf("[Dispatcher] Falha transitória para evento %s na integração %s. Agendando retry #%d em %v", ev.EventID, integID, attempts+1, backoff)
				_ = wp.redis.MarkDispatchRetry(ctx, integID, ev.EventID, attempts, backoff, callErr.Error())
				mu.Lock()
				allSettled = false
				mu.Unlock()
			}
		}(integ)
	}

	dispatchWG.Wait()

	// Só confirma XACK se todos os destinos foram resolvidos (ou sucesso, ou DLQ definitivo)
	if allSettled {
		_ = wp.redis.Client.XAck(ctx, stream, group, msg.ID).Err()
	}
}

type SiteIntegrationRecord struct {
	ID          string
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
		SELECT id::text, platform, credentials 
		FROM site_integrations 
		WHERE site_id = $1 AND is_active = true
	`, siteUUID)
	if err != nil {
		return results
	}
	defer rows.Close()

	for rows.Next() {
		var idStr string
		var platform string
		var credsJSON []byte
		if err := rows.Scan(&idStr, &platform, &credsJSON); err == nil {
			var credsMap map[string]string
			_ = json.Unmarshal(credsJSON, &credsMap)
			if idStr == "" {
				idStr = fmt.Sprintf("%s_%s", siteIDStr, platform)
			}
			results = append(results, SiteIntegrationRecord{
				ID:          idStr,
				Platform:    platform,
				Credentials: credsMap,
			})
		}
	}

	return results
}

// isPermanentError classifica se um erro é não transitório (fatal) e não deve ser retentado
func isPermanentError(err error) bool {
	if err == nil {
		return false
	}
	// Context timeout ou cancelamento são transitórios
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	// Timeouts de rede são transitórios
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return false
	}

	errStr := strings.ToLower(err.Error())

	// Palavras-chave indicativas de falhas transitórias
	if strings.Contains(errStr, "429") ||
		strings.Contains(errStr, "too many requests") ||
		strings.Contains(errStr, "rate limit") ||
		strings.Contains(errStr, "500") ||
		strings.Contains(errStr, "502") ||
		strings.Contains(errStr, "503") ||
		strings.Contains(errStr, "504") ||
		strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "no such host") ||
		strings.Contains(errStr, "broken pipe") {
		return false
	}

	// Erros HTTP 4xx (exceto 429) são falhas permanentes de configuração/credenciais/parâmetros
	if strings.Contains(errStr, "400") ||
		strings.Contains(errStr, "401") ||
		strings.Contains(errStr, "403") ||
		strings.Contains(errStr, "404") ||
		strings.Contains(errStr, "422") ||
		strings.Contains(errStr, "unauthorized") ||
		strings.Contains(errStr, "forbidden") ||
		strings.Contains(errStr, "invalid_client") ||
		strings.Contains(errStr, "invalid token") ||
		strings.Contains(errStr, "erro cliente http") {
		return true
	}

	return false
}

// calculateBackoff calcula atraso exponencial com jitter proporcional
func calculateBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}
	base := 1 * time.Second
	factor := math.Pow(2, float64(attempt-1))
	delay := time.Duration(float64(base) * factor)
	if delay > 60*time.Second {
		delay = 60 * time.Second
	}
	// Adiciona jitter de até 25%
	jitterMax := int64(delay / 4)
	if jitterMax > 0 {
		jitter := time.Duration(rand.Int63n(jitterMax))
		delay += jitter
	}
	return delay
}
