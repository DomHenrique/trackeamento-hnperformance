package identity

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"tracking-engine/internal/collector"
	"tracking-engine/internal/storage"
)

var nonDigitRegex = regexp.MustCompile(`\D`)

type Service struct {
	pg     *storage.PostgresDB
	redis  *storage.RedisClient
	ch     *storage.ClickHouseDB
	pepper string
}

func NewService(pg *storage.PostgresDB, rdb *storage.RedisClient, pepper string) *Service {
	return &Service{
		pg:     pg,
		redis:  rdb,
		pepper: pepper,
	}
}

// WithClickHouse associa a conexão do ClickHouse para suporte a mutações de exclusão (LGPD)
func (s *Service) WithClickHouse(ch *storage.ClickHouseDB) *Service {
	s.ch = ch
	return s
}

// NormalizeEmail converte para minúsculas e remove espaços em branco
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// NormalizePhone remove formatação e garante DDI 55 caso seja número brasileiro com DDD
func NormalizePhone(phone string) string {
	digits := nonDigitRegex.ReplaceAllString(phone, "")
	if len(digits) == 10 || len(digits) == 11 {
		digits = "55" + digits
	}
	return digits
}

// HashSHA256 calcula o hash hexadecimal SHA-256 padrão de uma string (utilizado por plataformas externas como Meta CAPI)
func HashSHA256(val string) string {
	if val == "" {
		return ""
	}
	h := sha256.Sum256([]byte(val))
	return hex.EncodeToString(h[:])
}

// HashHMACSHA256 calcula o HMAC-SHA256 com pepper secreto da aplicação para privacidade interna no banco relacional
func HashHMACSHA256(val, pepper string) string {
	if val == "" {
		return ""
	}
	if pepper == "" {
		return HashSHA256(val)
	}
	mac := hmac.New(sha256.New, []byte(pepper))
	mac.Write([]byte(val))
	return hex.EncodeToString(mac.Sum(nil))
}

// ReconcileAndRoute processa a identidade do visitante no PostgreSQL e encaminha conversões para o Dispatcher
func (s *Service) ReconcileAndRoute(ctx context.Context, ev *collector.EventPayload) error {
	if s.pg == nil || s.pg.Pool == nil {
		// Sem banco configurado, apenas encaminha conversões
		return s.checkAndRouteDispatch(ctx, ev, nil)
	}

	visitorID := strings.TrimSpace(ev.VisitorID)
	if visitorID == "" {
		return s.checkAndRouteDispatch(ctx, ev, nil)
	}

	siteUUID, err := uuid.Parse(ev.SiteID)
	if err != nil {
		log.Printf("[Identity] SiteID inválido (%s): %v", ev.SiteID, err)
		return s.checkAndRouteDispatch(ctx, ev, nil)
	}

	// Extração de dados de contato caso existam no payload
	var rawEmail, rawPhone, rawName string
	if ev.UserData != nil {
		if e, ok := ev.UserData["email"].(string); ok {
			rawEmail = NormalizeEmail(e)
		}
		if p, ok := ev.UserData["phone"].(string); ok {
			rawPhone = NormalizePhone(p)
		}
		if n, ok := ev.UserData["name"].(string); ok {
			rawName = strings.TrimSpace(n)
		}
	}

	emailHash := HashHMACSHA256(rawEmail, s.pepper)
	phoneHash := HashHMACSHA256(rawPhone, s.pepper)

	// 1. Verifica se visitante já existe para este site específico
	var existingVisitor struct {
		ID             uuid.UUID
		FirstGCLID     *string
		FirstFBCLID    *string
		FirstUTMSource *string
		EmailHash      *string
		PhoneHash      *string
	}

	query := `SELECT id, first_gclid, first_fbclid, first_utm_source, identified_email_hash, identified_phone_hash 
              FROM visitors WHERE site_id = $1 AND visitor_id = $2`
	err = s.pg.Pool.QueryRow(ctx, query, siteUUID, visitorID).Scan(
		&existingVisitor.ID,
		&existingVisitor.FirstGCLID,
		&existingVisitor.FirstFBCLID,
		&existingVisitor.FirstUTMSource,
		&existingVisitor.EmailHash,
		&existingVisitor.PhoneHash,
	)

	firstTouchMap := make(map[string]interface{})

	if err == pgx.ErrNoRows {
		// 2. Novo Visitante: Gravação com atribuição First-Touch e suporte a concorrência via ON CONFLICT
		insertSQL := `
			INSERT INTO visitors (
				site_id, visitor_id, first_seen_at, last_seen_at,
				first_landing_page, first_referrer,
				first_utm_source, first_utm_medium, first_utm_campaign, first_utm_content, first_utm_term,
				first_gclid, first_fbclid, first_ttclid,
				identified_name, identified_email_hash, identified_phone_hash
			) VALUES (
				$1, $2, now(), now(),
				$3, $4,
				$5, $6, $7, $8, $9,
				$10, $11, $12,
				$13, $14, $15
			)
			ON CONFLICT (site_id, visitor_id) DO UPDATE SET
				last_seen_at = now(),
				identified_name = COALESCE(NULLIF(EXCLUDED.identified_name, ''), visitors.identified_name),
				identified_email_hash = COALESCE(NULLIF(EXCLUDED.identified_email_hash, ''), visitors.identified_email_hash),
				identified_phone_hash = COALESCE(NULLIF(EXCLUDED.identified_phone_hash, ''), visitors.identified_phone_hash)
		`
		_, _ = s.pg.Pool.Exec(ctx, insertSQL,
			siteUUID, visitorID,
			ev.Attribution.LandingPage, ev.Attribution.Referrer,
			ev.Attribution.UTMSource, ev.Attribution.UTMMedium, ev.Attribution.UTMCampaign, ev.Attribution.UTMContent, ev.Attribution.UTMTerm,
			ev.Attribution.GCLID, ev.Attribution.FBCLID, ev.Attribution.TTCLID,
			rawName, emailHash, phoneHash,
		)

		firstTouchMap["landing_page"] = ev.Attribution.LandingPage
		firstTouchMap["utm_source"] = ev.Attribution.UTMSource
		firstTouchMap["utm_campaign"] = ev.Attribution.UTMCampaign
		firstTouchMap["gclid"] = ev.Attribution.GCLID
		firstTouchMap["fbclid"] = ev.Attribution.FBCLID
	} else if err == nil {
		// 3. Visitante Existente: Atualiza last_seen_at e unifica dados de contato caso novos
		updateSQL := `
			UPDATE visitors 
			SET last_seen_at = now(),
			    identified_name = COALESCE(NULLIF($3, ''), identified_name),
			    identified_email_hash = COALESCE(NULLIF($4, ''), identified_email_hash),
			    identified_phone_hash = COALESCE(NULLIF($5, ''), identified_phone_hash)
			WHERE site_id = $1 AND visitor_id = $2
		`
		_, _ = s.pg.Pool.Exec(ctx, updateSQL, siteUUID, visitorID, rawName, emailHash, phoneHash)

		if existingVisitor.FirstGCLID != nil {
			firstTouchMap["gclid"] = *existingVisitor.FirstGCLID
		}
		if existingVisitor.FirstFBCLID != nil {
			firstTouchMap["fbclid"] = *existingVisitor.FirstFBCLID
		}
		if existingVisitor.FirstUTMSource != nil {
			firstTouchMap["utm_source"] = *existingVisitor.FirstUTMSource
		}
	}

	// 4. Roteia para despacho server-side se for evento qualificado
	return s.checkAndRouteDispatch(ctx, ev, firstTouchMap)
}

// checkAndRouteDispatch verifica se o evento é uma conversão e envia para a fila de despacho do Meta/Google/CRM
func (s *Service) checkAndRouteDispatch(ctx context.Context, ev *collector.EventPayload, firstTouch map[string]interface{}) error {
	if ev.IsBot {
		log.Printf("[Identity] Interceptado: conversão '%s' originada de robô (motivo: %s). Despacho externo abortado para proteger Ads.", ev.EventName, ev.BotReason)
		return nil
	}

	// Gatekeeper de Privacidade: valida consentimento de marketing e sinal GPC
	if !ev.Consent.Marketing || ev.PrivacySignals.GPC {
		log.Printf("[Identity] Interceptado por compliance: conversão '%s' sem consentimento de marketing (marketing=%v, gpc=%v). Despacho externo cancelado.", ev.EventName, ev.Consent.Marketing, ev.PrivacySignals.GPC)
		return nil
	}

	name := strings.ToLower(ev.EventName)
	isConversion := name == "lead" || name == "purchase" || name == "whatsapp_click" || name == "form_submit" || name == "contact"

	if !isConversion {
		return nil
	}

	dispatchPayload := map[string]interface{}{
		"event":       ev,
		"first_touch": firstTouch,
		"queued_at":   time.Now().UTC(),
	}

	data, err := json.Marshal(dispatchPayload)
	if err != nil {
		return fmt.Errorf("erro ao serializar payload de despacho: %w", err)
	}

	return s.redis.PushDispatchEvent(ctx, data)
}

// PurgeResult armazena as estatísticas de execução de purga
type PurgeResult struct {
	TotalDeleted int64         `json:"total_deleted"`
	BatchesCount int           `json:"batches_count"`
	Duration     time.Duration `json:"duration"`
}

// PurgeInactiveVisitors remove visitantes inativos do PostgreSQL em lotes de 1.000 para evitar bloqueios de tabela
func (s *Service) PurgeInactiveVisitors(ctx context.Context, cutoffTime time.Time) (*PurgeResult, error) {
	if s.pg == nil || s.pg.Pool == nil {
		return &PurgeResult{}, nil
	}

	start := time.Now()
	var totalDeleted int64
	batches := 0
	batchSize := 1000

	query := `
		DELETE FROM visitors
		WHERE id IN (
			SELECT id FROM visitors
			WHERE last_seen_at < $1
			LIMIT $2
		)
	`

	for {
		if ctx != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}

		tag, err := s.pg.Pool.Exec(ctx, query, cutoffTime, batchSize)
		if err != nil {
			return nil, fmt.Errorf("falha ao executar lote de purga: %w", err)
		}

		rowsAffected := tag.RowsAffected()
		totalDeleted += rowsAffected
		batches++

		if rowsAffected < int64(batchSize) {
			break
		}

		// Pausa de 10ms entre lotes para cooperar com autovacuum e conexões concorrentes
		time.Sleep(10 * time.Millisecond)
	}

	return &PurgeResult{
		TotalDeleted: totalDeleted,
		BatchesCount: batches,
		Duration:     time.Since(start),
	}, nil
}

// PurgeVisitorData atende ao direito de eliminação do titular (Art. 18 LGPD)
func (s *Service) PurgeVisitorData(ctx context.Context, siteID, visitorID string) (int64, error) {
	siteUUID, err := uuid.Parse(siteID)
	if err != nil {
		return 0, fmt.Errorf("site_id inválido: %w", err)
	}

	visitorID = strings.TrimSpace(visitorID)
	if visitorID == "" {
		return 0, errors.New("visitor_id não pode ser vazio")
	}

	if s.pg == nil || s.pg.Pool == nil {
		return 0, nil
	}

	tag, err := s.pg.Pool.Exec(ctx, `
		DELETE FROM visitors
		WHERE site_id = $1 AND visitor_id = $2
	`, siteUUID, visitorID)
	if err != nil {
		return 0, fmt.Errorf("falha ao purgar visitante no Postgres: %w", err)
	}

	rows := tag.RowsAffected()
	log.Printf("[Identity/Compliance] Titular purgado com sucesso: site=%s (registros=%d)", siteID, rows)

	// Propaga mutação assíncrona no ClickHouse caso configurado
	if s.ch != nil && s.ch.Conn != nil {
		mutationQuery := `ALTER TABLE tracking_events.events DELETE WHERE site_id = $1 AND visitor_id = $2`
		_ = s.ch.Conn.Exec(ctx, mutationQuery, siteUUID, visitorID)
	}

	return rows, nil
}
