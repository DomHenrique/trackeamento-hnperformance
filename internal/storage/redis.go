package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
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

// PushDeadLetter enfileira mensagem na Dead-Letter Queue com metadados detalhados de diagnóstico e TTL de 24h
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
	pipe := r.Client.TxPipeline()
	pipe.XAdd(ctx, &redis.XAddArgs{
		Stream: stream,
		MaxLen: 50000,
		Approx: true,
		Values: map[string]interface{}{
			"payload": b,
		},
	})
	pipe.Expire(ctx, stream, 24*time.Hour)
	_, err = pipe.Exec(ctx)
	return err
}

// SaveEventHydratedUserData armazena os dados de contato hidratados no Redis para garantir preservação em retries
func (r *RedisClient) SaveEventHydratedUserData(ctx context.Context, eventID string, userData map[string]interface{}, ttl time.Duration) error {
	if r.Client == nil || eventID == "" || len(userData) == 0 {
		return nil
	}
	key := fmt.Sprintf("dispatch:event_user_data:%s", eventID)
	data, err := json.Marshal(userData)
	if err != nil {
		return err
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return r.Client.Set(ctx, key, data, ttl).Err()
}

// GetEventHydratedUserData recupera os dados de contato hidratados previamente para o evento
func (r *RedisClient) GetEventHydratedUserData(ctx context.Context, eventID string) (map[string]interface{}, error) {
	if r.Client == nil || eventID == "" {
		return nil, nil
	}
	key := fmt.Sprintf("dispatch:event_user_data:%s", eventID)
	data, err := r.Client.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}
	var res map[string]interface{}
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return res, nil
}

// CheckAndSetSubmissionDispatchedNX verifica e registra atomicamente se a submissão já foi enviada para a plataforma
func (r *RedisClient) CheckAndSetSubmissionDispatchedNX(ctx context.Context, platform, submissionID string) (bool, error) {
	if r.Client == nil || platform == "" || submissionID == "" {
		return true, nil
	}
	key := fmt.Sprintf("dispatched:sub:%s:%s", platform, submissionID)
	return r.Client.SetNX(ctx, key, "1", 24*time.Hour).Result()
}

// IsSubmissionDispatched verifica se a submissão já teve despacho concluído com sucesso para a plataforma
func (r *RedisClient) IsSubmissionDispatched(ctx context.Context, platform, submissionID string) (bool, error) {
	if r.Client == nil || platform == "" || submissionID == "" {
		return false, nil
	}
	key := fmt.Sprintf("dispatched:sub:%s:%s", platform, submissionID)
	exists, err := r.Client.Exists(ctx, key).Result()
	return exists > 0, err
}

// MarkSubmissionDispatched registra no Redis a conclusão com sucesso do despacho para a submissão na plataforma (TTL de 24h)
func (r *RedisClient) MarkSubmissionDispatched(ctx context.Context, platform, submissionID string, ttl time.Duration) error {
	if r.Client == nil || platform == "" || submissionID == "" {
		return nil
	}
	key := fmt.Sprintf("dispatched:sub:%s:%s", platform, submissionID)
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return r.Client.Set(ctx, key, "1", ttl).Err()
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

// GetOrCreateDailySalt obtém ou cria atomicamente o salt criptográfico para a data especificada (YYYY-MM-DD) com TTL de 48h
func (r *RedisClient) GetOrCreateDailySalt(ctx context.Context, dateStr string) (string, error) {
	if r.Client == nil {
		return "fallback_salt_32bytes_crypto_safe_hn_2026", nil
	}

	key := fmt.Sprintf("tracking:daily_salt:%s", dateStr)
	val, err := r.Client.Get(ctx, key).Result()
	if err == nil && val != "" {
		return val, nil
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("falha ao gerar salt criptografico: %w", err)
	}
	newSalt := hex.EncodeToString(b)

	set, err := r.Client.SetNX(ctx, key, newSalt, 48*time.Hour).Result()
	if err != nil {
		return "", err
	}
	if !set {
		return r.Client.Get(ctx, key).Result()
	}

	return newSalt, nil
}

// RoutingDecisionRecord representa um registro de auditoria e telemetria de despacho
type RoutingDecisionRecord struct {
	EventID      string          `json:"event_id"`
	EventName    string          `json:"event_name"`
	SiteID       string          `json:"site_id"`
	Timestamp    string          `json:"timestamp"`
	Platform     string          `json:"platform,omitempty"`
	Status       string          `json:"status"` // "success", "error", "blocked_consent", "blocked_gpc"
	StatusCode   int             `json:"status_code,omitempty"`
	Reason       string          `json:"reason,omitempty"`
	LatencyMs    int64           `json:"latency_ms,omitempty"`
	ConsentFlags map[string]bool `json:"consent_flags,omitempty"`
}

// RecordRoutingDecision incrementa contadores agregados e enfileira no buffer circular de auditoria recente
func (r *RedisClient) RecordRoutingDecision(ctx context.Context, siteID string, rec RoutingDecisionRecord) error {
	if r.Client == nil {
		return nil
	}

	if siteID == "" {
		siteID = "global"
	}
	if rec.Timestamp == "" {
		rec.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}

	today := time.Now().UTC().Format("20060102")
	payloadJSON, err := json.Marshal(rec)
	if err != nil {
		return err
	}

	pipe := r.Client.Pipeline()

	// 1. Contadores específicos do Site
	siteStatsKey := fmt.Sprintf("stats:routing:%s:%s", siteID, today)
	pipe.HIncrBy(ctx, siteStatsKey, "total", 1)
	pipe.HIncrBy(ctx, siteStatsKey, rec.Status, 1)
	if rec.Platform != "" {
		pipe.HIncrBy(ctx, siteStatsKey, fmt.Sprintf("%s:%s", rec.Platform, rec.Status), 1)
	}
	pipe.Expire(ctx, siteStatsKey, 7*24*time.Hour)

	// 2. Contadores Globais
	if siteID != "global" {
		globalStatsKey := fmt.Sprintf("stats:routing:global:%s", today)
		pipe.HIncrBy(ctx, globalStatsKey, "total", 1)
		pipe.HIncrBy(ctx, globalStatsKey, rec.Status, 1)
		if rec.Platform != "" {
			pipe.HIncrBy(ctx, globalStatsKey, fmt.Sprintf("%s:%s", rec.Platform, rec.Status), 1)
		}
		pipe.Expire(ctx, globalStatsKey, 7*24*time.Hour)
	}

	// 3. Buffer circular recente por Site (máximo 50 itens com TTL de 48h)
	siteRecentKey := fmt.Sprintf("routing:recent:%s", siteID)
	pipe.LPush(ctx, siteRecentKey, payloadJSON)
	pipe.LTrim(ctx, siteRecentKey, 0, 49)
	pipe.Expire(ctx, siteRecentKey, 48*time.Hour)

	// 4. Buffer circular recente Global
	if siteID != "global" {
		globalRecentKey := "routing:recent:global"
		pipe.LPush(ctx, globalRecentKey, payloadJSON)
		pipe.LTrim(ctx, globalRecentKey, 0, 49)
		pipe.Expire(ctx, globalRecentKey, 48*time.Hour)
	}

	_, err = pipe.Exec(ctx)
	return err
}

// GetRoutingSummary retorna o resumo consolidado de roteamento para a data de hoje (ou global)
func (r *RedisClient) GetRoutingSummary(ctx context.Context, siteID string) (map[string]interface{}, error) {
	if r.Client == nil {
		return map[string]interface{}{
			"total":           0,
			"success":         0,
			"error":           0,
			"blocked_consent": 0,
			"blocked_gpc":     0,
			"success_rate":    100.0,
			"destinations":    map[string]map[string]int64{},
		}, nil
	}

	if siteID == "" {
		siteID = "global"
	}

	today := time.Now().UTC().Format("20060102")
	statsKey := fmt.Sprintf("stats:routing:%s:%s", siteID, today)

	data, err := r.Client.HGetAll(ctx, statsKey).Result()
	if err != nil && err != redis.Nil {
		return nil, err
	}

	var total, success, errCount, blockedConsent, blockedGPC int64
	destinations := make(map[string]map[string]int64)

	for k, vStr := range data {
		val, _ := strconv.ParseInt(vStr, 10, 64)
		switch k {
		case "total":
			total = val
		case "success":
			success = val
		case "error":
			errCount = val
		case "blocked_consent":
			blockedConsent = val
		case "blocked_gpc":
			blockedGPC = val
		default:
			// Pode ser "platform:status"
			parts := strings.Split(k, ":")
			if len(parts) == 2 {
				plat := parts[0]
				stat := parts[1]
				if destinations[plat] == nil {
					destinations[plat] = make(map[string]int64)
				}
				destinations[plat][stat] = val
			}
		}
	}

	var successRate float64 = 100.0
	dispatched := success + errCount
	if dispatched > 0 {
		successRate = float64(success) / float64(dispatched) * 100.0
	}

	return map[string]interface{}{
		"site_id":          siteID,
		"date":             today,
		"total":            total,
		"success":          success,
		"error":            errCount,
		"blocked_consent":  blockedConsent,
		"blocked_gpc":      blockedGPC,
		"total_blocked":    blockedConsent + blockedGPC,
		"success_rate":     fmt.Sprintf("%.1f", successRate),
		"destinations":     destinations,
	}, nil
}

// GetRecentRoutingDecisions recupera a lista de decisões recentes do buffer circular
func (r *RedisClient) GetRecentRoutingDecisions(ctx context.Context, siteID string, limit int64) ([]RoutingDecisionRecord, error) {
	if r.Client == nil {
		return []RoutingDecisionRecord{}, nil
	}

	if siteID == "" {
		siteID = "global"
	}
	if limit <= 0 || limit > 50 {
		limit = 50
	}

	recentKey := fmt.Sprintf("routing:recent:%s", siteID)
	rawItems, err := r.Client.LRange(ctx, recentKey, 0, limit-1).Result()
	if err != nil && err != redis.Nil {
		return nil, err
	}

	results := make([]RoutingDecisionRecord, 0, len(rawItems))
	for _, raw := range rawItems {
		var rec RoutingDecisionRecord
		if err := json.Unmarshal([]byte(raw), &rec); err == nil {
			results = append(results, rec)
		}
	}

	return results, nil
}

// LeadCacheConsent modela as permissões de consentimento associadas ao lead em cache
type LeadCacheConsent struct {
	Necessary bool `json:"necessary"`
	Analytics bool `json:"analytics"`
	Marketing bool `json:"marketing"`
}

// LeadCacheRecord armazena os dados de contato e consentimento de uma submissão de formulário
type LeadCacheRecord struct {
	SubmissionID        string                 `json:"submission_id"`
	SiteID              string                 `json:"site_id"`
	UserData            map[string]interface{} `json:"user_data"`
	Consent             LeadCacheConsent       `json:"consent"`
	ExplicitLeadConsent bool                   `json:"explicit_lead_consent"`
	ConsentStatus       string                 `json:"consent_status,omitempty"` // "granted", "denied", "pending"
	CachedAt            time.Time              `json:"cached_at"`
}

// SaveLeadCache armazena dados de contato temporários de uma submissão no Redis com TTL de 15 minutos
func (r *RedisClient) SaveLeadCache(ctx context.Context, record *LeadCacheRecord) error {
	if r.Client == nil || record == nil || record.SiteID == "" || record.SubmissionID == "" {
		return nil
	}
	if record.CachedAt.IsZero() {
		record.CachedAt = time.Now().UTC()
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("erro serializando LeadCacheRecord: %w", err)
	}
	key := fmt.Sprintf("tracking:lead_cache:%s:%s", record.SiteID, record.SubmissionID)
	return r.Client.Set(ctx, key, data, 15*time.Minute).Err()
}

// GetLeadCache recupera dados de contato temporários de uma submissão do Redis
func (r *RedisClient) GetLeadCache(ctx context.Context, siteID, submissionID string) (*LeadCacheRecord, error) {
	if r.Client == nil || siteID == "" || submissionID == "" {
		return nil, nil
	}
	key := fmt.Sprintf("tracking:lead_cache:%s:%s", siteID, submissionID)
	val, err := r.Client.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}
	var record LeadCacheRecord
	if err := json.Unmarshal(val, &record); err != nil {
		return nil, fmt.Errorf("erro deserializando LeadCacheRecord: %w", err)
	}
	return &record, nil
}

// DeleteLeadCache purga explicitamente os dados de contato em conformidade com a LGPD
func (r *RedisClient) DeleteLeadCache(ctx context.Context, siteID, submissionID string) error {
	if r.Client == nil || siteID == "" || submissionID == "" {
		return nil
	}
	key := fmt.Sprintf("tracking:lead_cache:%s:%s", siteID, submissionID)
	return r.Client.Del(ctx, key).Err()
}

// AcquireSubmissionDispatchLease obtém lease de submissão por integração, garantindo que múltiplos
// estágios da mesma submissão (ex: form_client_validated e form_submit_success) não gerem disparos duplicados.
func (r *RedisClient) AcquireSubmissionDispatchLease(ctx context.Context, integrationID, submissionID string, leaseDuration time.Duration) (acquired bool, alreadySucceeded bool, err error) {
	if r.Client == nil || submissionID == "" {
		return true, false, nil
	}
	key := fmt.Sprintf("dispatch:sub_lock:%s:%s", integrationID, submissionID)
	now := time.Now().UTC()
	nowMs := now.UnixMilli()

	vals, err := r.Client.HGetAll(ctx, key).Result()
	if err != nil && err != redis.Nil {
		return false, false, err
	}

	parseMs := func(val string) int64 {
		v, _ := strconv.ParseInt(val, 10, 64)
		if v > 0 && v < 100000000000 {
			return v * 1000
		}
		return v
	}

	if len(vals) == 0 {
		pipe := r.Client.TxPipeline()
		pipe.HSet(ctx, key, map[string]interface{}{
			"status":           "processing",
			"lease_expires_at": now.Add(leaseDuration).UnixMilli(),
		})
		pipe.Expire(ctx, key, 24*time.Hour)
		_, err = pipe.Exec(ctx)
		if err != nil {
			return false, false, err
		}
		return true, false, nil
	}

	status := vals["status"]
	if status == "succeeded" {
		return false, true, nil
	}

	if status == "processing" {
		leaseExpMs := parseMs(vals["lease_expires_at"])
		if leaseExpMs > nowMs {
			return false, false, nil
		}
		// Lease expirou sem conclusão; renova lease
		r.Client.HSet(ctx, key, map[string]interface{}{
			"status":           "processing",
			"lease_expires_at": now.Add(leaseDuration).UnixMilli(),
		})
		return true, false, nil
	}

	return true, false, nil
}

// MarkSubmissionDispatchSuccess registra a conclusão com êxito do despacho para aquela submissão e integração
func (r *RedisClient) MarkSubmissionDispatchSuccess(ctx context.Context, integrationID, submissionID string) error {
	if r.Client == nil || submissionID == "" {
		return nil
	}
	key := fmt.Sprintf("dispatch:sub_lock:%s:%s", integrationID, submissionID)
	pipe := r.Client.TxPipeline()
	pipe.HSet(ctx, key, map[string]interface{}{
		"status":       "succeeded",
		"succeeded_at": time.Now().UTC().UnixMilli(),
	})
	pipe.Expire(ctx, key, 24*time.Hour)
	_, err := pipe.Exec(ctx)
	return err
}


