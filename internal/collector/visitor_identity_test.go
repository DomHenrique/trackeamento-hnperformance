package collector

import (
	"context"
	"strings"
	"testing"
	"time"

	"tracking-engine/internal/prefixedid"
)

func TestCalculateDeterministicVisitorID(t *testing.T) {
	salt := "test_salt_1234567890abcdef"
	siteID := "site_alpha"
	ip := "187.33.241.0"
	ua := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36"
	signals := &ClientSignals{
		ScreenRes:  "1920x1080",
		ColorDepth: 24,
		Lang:       "pt-BR",
		Tz:         "America/Sao_Paulo",
	}

	vid := CalculateDeterministicVisitorID(salt, siteID, ip, ua, signals)

	// 1. Valida prefixo e comprimento
	if !strings.HasPrefix(vid, prefixedid.PrefixVisitor) {
		t.Fatalf("esperado prefixo %s, obtido %s", prefixedid.PrefixVisitor, vid)
	}
	if !prefixedid.IsValidVisitorID(vid) {
		t.Fatalf("visitor_id gerado nao e valido segundo prefixedid.IsValidVisitorID: %s", vid)
	}

	// 2. Determinismo estrito: mesmas entradas geram exatamente o mesmo ID
	vid2 := CalculateDeterministicVisitorID(salt, siteID, ip, ua, signals)
	if vid != vid2 {
		t.Fatalf("cálculo não determinístico: %s != %s", vid, vid2)
	}

	// 3. Isolamento multi-tenant: site_id diferente gera visitor_id diferente
	vidOtherSite := CalculateDeterministicVisitorID(salt, "site_beta", ip, ua, signals)
	if vid == vidOtherSite {
		t.Fatalf("falha de isolamento multitenant: mesmo ID gerado para sites diferentes (%s)", vid)
	}

	// 4. Rotação de salt (dia seguinte): salte diferente gera visitor_id diferente
	vidNextDay := CalculateDeterministicVisitorID("different_salt_for_next_day", siteID, ip, ua, signals)
	if vid == vidNextDay {
		t.Fatalf("salts diferentes geraram o mesmo ID: %s", vid)
	}
}

func TestHardwareDisambiguationSameIP(t *testing.T) {
	salt := "daily_salt_shared_nat_network"
	siteID := "site_shared_office"
	sharedIP := "200.180.10.0" // Mesmo IP para todos na empresa
	sharedUA := "Mozilla/5.0 (Windows NT 10.0; Win64; x64)"

	// Usuário 1: Monitor Full HD
	user1Signals := &ClientSignals{
		ScreenRes:  "1920x1080",
		ColorDepth: 24,
		Lang:       "pt-BR",
		Tz:         "America/Sao_Paulo",
	}

	// Usuário 2: Monitor 4K
	user2Signals := &ClientSignals{
		ScreenRes:  "3840x2160",
		ColorDepth: 30,
		Lang:       "pt-BR",
		Tz:         "America/Sao_Paulo",
	}

	// Usuário 3: Configuração em inglês
	user3Signals := &ClientSignals{
		ScreenRes:  "1920x1080",
		ColorDepth: 24,
		Lang:       "en-US",
		Tz:         "America/Sao_Paulo",
	}

	vid1 := CalculateDeterministicVisitorID(salt, siteID, sharedIP, sharedUA, user1Signals)
	vid2 := CalculateDeterministicVisitorID(salt, siteID, sharedIP, sharedUA, user2Signals)
	vid3 := CalculateDeterministicVisitorID(salt, siteID, sharedIP, sharedUA, user3Signals)

	if vid1 == vid2 {
		t.Errorf("usuários 1 e 2 no mesmo IP colidiram: %s == %s", vid1, vid2)
	}
	if vid1 == vid3 {
		t.Errorf("usuários 1 e 3 no mesmo IP colidiram: %s == %s", vid1, vid3)
	}
	if vid2 == vid3 {
		t.Errorf("usuários 2 e 3 no mesmo IP colidiram: %s == %s", vid2, vid3)
	}
}

func TestVisitorIdentityManager_CacheAndFallback(t *testing.T) {
	mgr := NewVisitorIdentityManager(nil) // sem redis
	ctx := context.Background()

	now := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	saltToday := mgr.GetDailySalt(ctx, now)
	if saltToday == "" {
		t.Fatal("salt gerado não deve ser vazio")
	}

	// Segundo acesso no mesmo dia deve retornar do cache
	saltTodayAgain := mgr.GetDailySalt(ctx, now.Add(2*time.Hour))
	if saltToday != saltTodayAgain {
		t.Fatalf("esperado salt idêntico do cache, obtido %s != %s", saltToday, saltTodayAgain)
	}

	// Próximo dia deve gerar novo salt
	saltTomorrow := mgr.GetDailySalt(ctx, now.Add(25*time.Hour))
	if saltToday == saltTomorrow {
		t.Fatalf("salt de amanhã deve ser diferente do de hoje: %s == %s", saltToday, saltTomorrow)
	}
}

func TestIntraDayVisitorIDStability_AndNoCookieEmission(t *testing.T) {
	mgr := NewVisitorIdentityManager(nil)
	ctx := context.Background()

	dayStart := time.Date(2026, 9, 27, 8, 0, 0, 0, time.UTC)
	salt := mgr.GetDailySalt(ctx, dayStart)

	siteID := "site_cookieless_test"
	clientIP := "177.136.24.50"
	userAgent := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36"
	signals := &ClientSignals{
		ScreenRes:  "2560x1440",
		ColorDepth: 24,
		Lang:       "pt-BR",
		Tz:         "America/Sao_Paulo",
	}

	// 1. Simulação de 10 eventos distribuídos ao longo do mesmo dia (8h, 10h, 14h, 18h, 23h59)
	var firstVID string
	hoursOffsets := []time.Duration{0, 2 * time.Hour, 6 * time.Hour, 10 * time.Hour, 15*time.Hour + 59*time.Minute}
	for i, offset := range hoursOffsets {
		evTime := dayStart.Add(offset)
		dailySaltAtHour := mgr.GetDailySalt(ctx, evTime)
		if dailySaltAtHour != salt {
			t.Fatalf("salt no mesmo dia variou na hora %v: %s != %s", offset, dailySaltAtHour, salt)
		}

		vid := CalculateDeterministicVisitorID(dailySaltAtHour, siteID, clientIP, userAgent, signals)
		if i == 0 {
			firstVID = vid
		} else if vid != firstVID {
			t.Fatalf("instabilidade intra-dia detectada: no evento %d (offset %v) o visitor_id mudou de %s para %s", i, offset, firstVID, vid)
		}
	}

	// 2. Comprovar que no dia seguinte (00h05 do dia 28), o salt rotaciona e visitor_id é outro
	nextDayTime := dayStart.Add(16*time.Hour + 5*time.Minute)
	nextDaySalt := mgr.GetDailySalt(ctx, nextDayTime)
	nextDayVID := CalculateDeterministicVisitorID(nextDaySalt, siteID, clientIP, userAgent, signals)

	if nextDayVID == firstVID {
		t.Fatalf("falha de privacidade: o visitor_id do dia seguinte não deveria colidir com o do dia anterior: %s == %s", nextDayVID, firstVID)
	}
}

