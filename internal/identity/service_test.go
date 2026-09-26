package identity

import (
	"context"
	"testing"
	"time"

	"tracking-engine/internal/collector"
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

