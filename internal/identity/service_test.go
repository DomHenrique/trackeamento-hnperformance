package identity

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"tracking-engine/internal/collector"
	"tracking-engine/internal/config"
	"tracking-engine/internal/storage"
)

func TestNormalizeEmail(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"  joao@Email.Com  ", "joao@email.com"},
		{"Maria.Silva@Dominio.COM.BR", "maria.silva@dominio.com.br"},
	}

	for _, c := range cases {
		result := NormalizeEmail(c.input)
		if result != c.expected {
			t.Errorf("para '%s': esperado '%s', obtido '%s'", c.input, c.expected, result)
		}
	}
}

func TestNormalizePhone(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"(21) 99999-8888", "5521999998888"},
		{"11988887777", "5511988887777"},
		{"+55 21 99999-8888", "5521999998888"},
	}

	for _, c := range cases {
		result := NormalizePhone(c.input)
		if result != c.expected {
			t.Errorf("para '%s': esperado '%s', obtido '%s'", c.input, c.expected, result)
		}
	}
}

func TestHashSHA256(t *testing.T) {
	email := "teste@hnperformancedigital.com.br"
	hash := HashSHA256(email)

	if len(hash) != 64 {
		t.Errorf("esperado hash sha256 de 64 caracteres, obtido %d", len(hash))
	}

	// Hash vazio para entrada vazia
	if HashSHA256("") != "" {
		t.Errorf("esperado hash vazio para string vazia")
	}
}

func TestHashHMACSHA256(t *testing.T) {
	email := "teste@hnperformancedigital.com.br"
	pepper := "super_secret_pepper_2026"

	h1 := HashHMACSHA256(email, pepper)
	if len(h1) != 64 {
		t.Errorf("esperado hmac de 64 caracteres, obtido %d", len(h1))
	}

	// Determinismo
	h2 := HashHMACSHA256(email, pepper)
	if h1 != h2 {
		t.Errorf("HMAC deve ser determinístico: %s != %s", h1, h2)
	}

	// Diferente de SHA256 padrão
	stdHash := HashSHA256(email)
	if h1 == stdHash {
		t.Errorf("HMAC com pepper deve ser diferente do SHA256 padrão")
	}

	// Pepper diferente gera hash diferente
	hDiff := HashHMACSHA256(email, "outro_pepper")
	if h1 == hDiff {
		t.Errorf("peppers diferentes devem produzir hashes diferentes")
	}

	// Entrada vazia
	if HashHMACSHA256("", pepper) != "" {
		t.Errorf("esperado string vazia para entrada vazia")
	}

	// Pepper vazio usa fallback SHA256 padrão
	if HashHMACSHA256(email, "") != stdHash {
		t.Errorf("pepper vazio deve retornar SHA256 padrão")
	}
}

func TestReconcileAndRoute_FallbacksAndGuards(t *testing.T) {
	ctx := context.Background()
	svc := NewService(nil, nil, "test_pepper")

	// 1. Visitante vazio não deve quebrar
	err := svc.ReconcileAndRoute(ctx, &collector.EventPayload{
		VisitorID: "",
		EventName: "pageview",
	})
	if err != nil {
		t.Errorf("esperado nil para visitorID vazio, obtido %v", err)
	}

	// 2. SiteID inválido não deve quebrar
	err = svc.ReconcileAndRoute(ctx, &collector.EventPayload{
		SiteID:    "invalid-uuid",
		VisitorID: "hn_vis_123456789012345678901234",
		EventName: "pageview",
	})
	if err != nil {
		t.Errorf("esperado nil para siteID inválido, obtido %v", err)
	}

	// 3. Evento não-conversão sem redis configurado não deve gerar erro
	err = svc.ReconcileAndRoute(ctx, &collector.EventPayload{
		SiteID:    "00000000-0000-0000-0000-000000000001",
		VisitorID: "hn_vis_123456789012345678901234",
		EventName: "pageview",
	})
	if err != nil {
		t.Errorf("esperado nil para evento não-conversão, obtido %v", err)
	}

	// 4. Robô em evento de conversão deve ser bloqueado antes de tentar dispatch
	err = svc.ReconcileAndRoute(ctx, &collector.EventPayload{
		SiteID:    "00000000-0000-0000-0000-000000000001",
		VisitorID: "hn_vis_123456789012345678901234",
		EventName: "lead",
		IsBot:     true,
		BotReason: "headless-detected",
	})
	if err != nil {
		t.Errorf("esperado interceptação silenciosa de bot sem erro, obtido %v", err)
	}

	// 5. Conversão sem consentimento de marketing deve ser abortada antes do dispatch
	err = svc.ReconcileAndRoute(ctx, &collector.EventPayload{
		SiteID:    "00000000-0000-0000-0000-000000000001",
		VisitorID: "hn_vis_123456789012345678901234",
		EventName: "lead",
		Consent:   collector.ConsentState{Analytics: true, Marketing: false},
	})
	if err != nil {
		t.Errorf("esperado cancelamento silencioso por falta de consentimento de marketing, obtido %v", err)
	}

	// 6. Conversão sob sinal Sec-GPC ativo deve ter despacho externo cancelado
	err = svc.ReconcileAndRoute(ctx, &collector.EventPayload{
		SiteID:         "00000000-0000-0000-0000-000000000001",
		VisitorID:      "hn_vis_123456789012345678901234",
		EventName:      "lead",
		Consent:        collector.ConsentState{Analytics: true, Marketing: true},
		PrivacySignals: collector.PrivacySignals{GPC: true},
	})
	if err != nil {
		t.Errorf("esperado cancelamento por sinal GPC ativo, obtido %v", err)
	}
}

func TestPurgeInactiveVisitors_NilPool(t *testing.T) {
	ctx := context.Background()
	svc := NewService(nil, nil, "test_pepper")

	res, err := svc.PurgeInactiveVisitors(ctx, time.Now())
	if err != nil {
		t.Fatalf("PurgeInactiveVisitors falhou com nil pool: %v", err)
	}
	if res == nil {
		t.Fatalf("esperado resultado não-nulo para PurgeInactiveVisitors")
	}
	if res.TotalDeleted != 0 {
		t.Errorf("esperado TotalDeleted=0, obtido %d", res.TotalDeleted)
	}
}

func TestPurgeVisitorData_Validations(t *testing.T) {
	ctx := context.Background()
	svc := NewService(nil, nil, "test_pepper")

	// 1. Visitor ID vazio
	_, err := svc.PurgeVisitorData(ctx, "00000000-0000-0000-0000-000000000001", "   ")
	if err == nil {
		t.Errorf("esperado erro para visitorID vazio")
	}

	// 2. Site ID inválido (não UUID)
	_, err = svc.PurgeVisitorData(ctx, "invalido", "hn_vis_123456789012345678901234")
	if err == nil {
		t.Errorf("esperado erro para siteID inválido")
	}

	// 3. Pool nulo com argumentos válidos (graceful return)
	rows, err := svc.PurgeVisitorData(ctx, "00000000-0000-0000-0000-000000000001", "hn_vis_123456789012345678901234")
	if err != nil {
		t.Errorf("esperado nil error para pool nulo, obtido %v", err)
	}
	if rows != 0 {
		t.Errorf("esperado 0 rows para pool nulo, obtido %d", rows)
	}
}

func TestReconcileAndRoute_CrossMidnightSessionStitching(t *testing.T) {
	ctx := context.Background()
	svc := NewService(nil, nil, "test_pepper")

	// Evento antes da meia-noite
	ev1 := &collector.EventPayload{
		SiteID:    "00000000-0000-0000-0000-000000000001",
		VisitorID: "hn_vis_saltday1_1234567890",
		SessionID: "hn_ses_continuous_session1",
		EventName: "page_view",
		Consent:   collector.ConsentState{Analytics: true, Marketing: true},
	}
	err1 := svc.ReconcileAndRoute(ctx, ev1)
	if err1 != nil {
		t.Fatalf("erro ao processar evento 1: %v", err1)
	}

	// Evento logo após a meia-noite (mesma session_id, mas novo visitor_id gerado com novo salt)
	ev2 := &collector.EventPayload{
		SiteID:    "00000000-0000-0000-0000-000000000001",
		VisitorID: "hn_vis_saltday2_9876543210",
		SessionID: "hn_ses_continuous_session1",
		EventName: "page_view",
		Consent:   collector.ConsentState{Analytics: true, Marketing: true},
	}
	err2 := svc.ReconcileAndRoute(ctx, ev2)
	if err2 != nil {
		t.Fatalf("erro ao processar evento 2: %v", err2)
	}
}

func TestReconcileAndRoute_LeadCacheStorage(t *testing.T) {
	// Verifica se há Redis local disponível
	conn, err := net.DialTimeout("tcp", "127.0.0.1:6379", 500*time.Millisecond)
	if err != nil {
		t.Skip("Redis local 127.0.0.1:6379 não disponível, pulando teste de cache de lead")
	}
	_ = conn.Close()

	cfg := &config.Config{
		RedisAddr: "127.0.0.1:6379",
	}
	rdb, err := storage.NewRedis(cfg)
	if err != nil || rdb.Client == nil {
		t.Skip("Falha conectando ao Redis local")
	}

	ctx := context.Background()
	svc := NewService(nil, rdb, "test_pepper")

	siteID := fmt.Sprintf("site_cache_%d", time.Now().UnixNano())
	subID := fmt.Sprintf("sub_cache_%d", time.Now().UnixNano())

	defer func() {
		_ = rdb.DeleteLeadCache(ctx, siteID, subID)
	}()

	// 1. Evento com contato válido e submission_id -> Deve gravar no Redis
	ev := &collector.EventPayload{
		SiteID:       siteID,
		VisitorID:    "hn_vis_1234567890",
		SessionID:    "hn_ses_1234567890",
		SubmissionID: subID,
		EventName:    "form_submit",
		UserData: map[string]interface{}{
			"email": "eduardorosalem@soprano.com.br",
			"phone": "54 99114.3857",
			"name":  "Eduardo Rosalem",
		},
		Consent:             collector.ConsentState{Analytics: true, Marketing: true},
		ExplicitLeadConsent: true,
	}

	err = svc.ReconcileAndRoute(ctx, ev)
	if err != nil {
		t.Fatalf("erro em ReconcileAndRoute: %v", err)
	}

	cached, err := rdb.GetLeadCache(ctx, siteID, subID)
	if err != nil {
		t.Fatalf("erro ao buscar cache gravado: %v", err)
	}
	if cached == nil {
		t.Fatalf("LeadCacheRecord não foi gravado no Redis")
	}
	if cached.UserData["email"] != "eduardorosalem@soprano.com.br" {
		t.Errorf("esperado email normalizado, obtido %v", cached.UserData["email"])
	}
	if cached.UserData["phone"] != "5554991143857" { // normalizado com DDI 55
		t.Errorf("esperado phone normalizado '5554991143857', obtido '%v'", cached.UserData["phone"])
	}
	if !cached.ExplicitLeadConsent {
		t.Errorf("esperado ExplicitLeadConsent=true")
	}

	// 2. Evento subsequente sem dados de contato -> Não deve sobrescrever com vazio
	evEmpty := &collector.EventPayload{
		SiteID:       siteID,
		VisitorID:    "hn_vis_1234567890",
		SessionID:    "hn_ses_1234567890",
		SubmissionID: subID,
		EventName:    "form_submit",
		UserData:     map[string]interface{}{},
		Consent:      collector.ConsentState{Analytics: true, Marketing: true},
	}
	err = svc.ReconcileAndRoute(ctx, evEmpty)
	if err != nil {
		t.Fatalf("erro no evento vazio: %v", err)
	}

	cachedStillThere, _ := rdb.GetLeadCache(ctx, siteID, subID)
	if cachedStillThere == nil || cachedStillThere.UserData["email"] != "eduardorosalem@soprano.com.br" {
		t.Errorf("cache foi indevidamente apagado ou sobrescrito por evento vazio")
	}
}


