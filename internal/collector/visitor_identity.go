package collector

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"tracking-engine/internal/prefixedid"
	"tracking-engine/internal/storage"
)

// VisitorIdentityManager gerencia a resolução e rotação diária de identidade server-side cookieless
type VisitorIdentityManager struct {
	redis     *storage.RedisClient
	saltCache sync.Map // dateStr -> salt
}

// NewVisitorIdentityManager cria uma nova instância do gerenciador de identidade
func NewVisitorIdentityManager(redis *storage.RedisClient) *VisitorIdentityManager {
	return &VisitorIdentityManager{
		redis: redis,
	}
}

// GetDailySalt obtém o salt criptográfico para a data informada (UTC), utilizando cache em memória
func (m *VisitorIdentityManager) GetDailySalt(ctx context.Context, t time.Time) string {
	dateStr := t.UTC().Format("2006-01-02")
	if cached, ok := m.saltCache.Load(dateStr); ok {
		if s, ok := cached.(string); ok && s != "" {
			return s
		}
	}

	if m.redis != nil {
		fastCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		salt, err := m.redis.GetOrCreateDailySalt(fastCtx, dateStr)
		if err == nil && salt != "" {
			m.saltCache.Store(dateStr, salt)
			return salt
		}
	}

	// Fallback autônomo em memória se Redis indisponível
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	fallback := hex.EncodeToString(b)
	m.saltCache.Store(dateStr, fallback)
	return fallback
}

// FormatHardwareFingerprint extrai e normaliza sinais leves de hardware do cliente
func FormatHardwareFingerprint(signals *ClientSignals) string {
	if signals == nil {
		return ""
	}

	screenRes := strings.TrimSpace(signals.ScreenRes)
	if screenRes == "" && (signals.ScreenW > 0 || signals.ScreenH > 0) {
		screenRes = fmt.Sprintf("%dx%d", signals.ScreenW, signals.ScreenH)
	}

	lang := strings.ToLower(strings.TrimSpace(signals.Lang))
	tz := strings.TrimSpace(signals.Tz)

	return fmt.Sprintf("%s|%d|%s|%s", screenRes, signals.ColorDepth, lang, tz)
}

// CalculateDeterministicVisitorID calcula o visitor_id via HMAC-SHA256 respeitando o prefixo hn_vis_ e 24 caracteres hex
func CalculateDeterministicVisitorID(salt string, siteID, maskedIP, userAgent string, signals *ClientSignals) string {
	if salt == "" {
		salt = "fallback_empty_salt_hn_2026"
	}

	hw := FormatHardwareFingerprint(signals)
	raw := fmt.Sprintf("%s|%s|%s|%s", strings.TrimSpace(siteID), strings.TrimSpace(maskedIP), strings.TrimSpace(userAgent), hw)

	h := hmac.New(sha256.New, []byte(salt))
	h.Write([]byte(raw))
	sum := hex.EncodeToString(h.Sum(nil))

	// Prefix hn_vis_ seguido pelos primeiros 24 caracteres hexadecimais
	hash24 := sum[:24]
	return prefixedid.PrefixVisitor + hash24
}

// ResolveVisitorID resolve o identificador determinístico do visitante para o evento
func (m *VisitorIdentityManager) ResolveVisitorID(ctx context.Context, t time.Time, siteID, maskedIP, userAgent string, signals *ClientSignals) string {
	salt := m.GetDailySalt(ctx, t)
	return CalculateDeterministicVisitorID(salt, siteID, maskedIP, userAgent, signals)
}
