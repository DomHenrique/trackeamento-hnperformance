package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	pg    *storage.PostgresDB
	redis *storage.RedisClient
}

func NewService(pg *storage.PostgresDB, rdb *storage.RedisClient) *Service {
	return &Service{
		pg:    pg,
		redis: rdb,
	}
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

// HashSHA256 calcula o hash hexadecimal SHA-256 de uma string
func HashSHA256(val string) string {
	if val == "" {
		return ""
	}
	h := sha256.Sum256([]byte(val))
	return hex.EncodeToString(h[:])
}

// ReconcileAndRoute processa a identidade do visitante no PostgreSQL e encaminha conversões para o Dispatcher
func (s *Service) ReconcileAndRoute(ctx context.Context, ev *collector.EventPayload) error {
	if s.pg == nil || s.pg.Pool == nil {
		// Sem banco configurado, apenas encaminha conversões
		return s.checkAndRouteDispatch(ctx, ev, nil)
	}

	rawUUID := strings.TrimPrefix(ev.VisitorID, "v_")
	visitorUUID, err := uuid.Parse(rawUUID)
	if err != nil {
		// Gera UUID determinístico caso não seja parseável
		visitorUUID = uuid.New()
	}

	siteUUID, _ := uuid.Parse(ev.SiteID)

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

	emailHash := HashSHA256(rawEmail)
	phoneHash := HashSHA256(rawPhone)

	// 1. Verifica se visitante já existe
	var existingVisitor struct {
		ID             uuid.UUID
		FirstGCLID     *string
		FirstFBCLID    *string
		FirstUTMSource *string
		EmailHash      *string
		PhoneHash      *string
	}

	query := `SELECT id, first_gclid, first_fbclid, first_utm_source, identified_email_hash, identified_phone_hash 
              FROM visitors WHERE id = $1`
	err = s.pg.Pool.QueryRow(ctx, query, visitorUUID).Scan(
		&existingVisitor.ID,
		&existingVisitor.FirstGCLID,
		&existingVisitor.FirstFBCLID,
		&existingVisitor.FirstUTMSource,
		&existingVisitor.EmailHash,
		&existingVisitor.PhoneHash,
	)

	firstTouchMap := make(map[string]interface{})

	if err == pgx.ErrNoRows {
		// 2. Novo Visitante: Gravação com atribuição First-Touch
		insertSQL := `
			INSERT INTO visitors (
				id, site_id, first_seen_at, last_seen_at,
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
		`
		_, _ = s.pg.Pool.Exec(ctx, insertSQL,
			visitorUUID, siteUUID,
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
			    identified_name = COALESCE(NULLIF($2, ''), identified_name),
			    identified_email_hash = COALESCE(NULLIF($3, ''), identified_email_hash),
			    identified_phone_hash = COALESCE(NULLIF($4, ''), identified_phone_hash)
			WHERE id = $1
		`
		_, _ = s.pg.Pool.Exec(ctx, updateSQL, visitorUUID, rawName, emailHash, phoneHash)

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
