package storage

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"tracking-engine/internal/config"
)

func setupTestRedis(t *testing.T) (*RedisClient, context.Context) {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:6379", 500*time.Millisecond)
	if err != nil {
		t.Skip("Redis local 127.0.0.1:6379 indisponível, pulando teste")
	}
	_ = conn.Close()

	cfg := &config.Config{
		RedisAddr: "127.0.0.1:6379",
	}
	rdb, err := NewRedis(cfg)
	if err != nil || rdb.Client == nil {
		t.Skip("Falha conectando ao Redis local")
	}
	return rdb, context.Background()
}

func TestLeadCacheLifecycle(t *testing.T) {
	rdb, ctx := setupTestRedis(t)

	siteID := fmt.Sprintf("site_test_%d", time.Now().UnixNano())
	subID := fmt.Sprintf("sub_test_%d", time.Now().UnixNano())

	defer func() {
		_ = rdb.DeleteLeadCache(ctx, siteID, subID)
	}()

	rec := &LeadCacheRecord{
		SubmissionID: subID,
		SiteID:       siteID,
		UserData: map[string]interface{}{
			"email": "eduardo@teste.com",
			"phone": "54991143857",
			"name":  "Eduardo Teste",
		},
		Consent: LeadCacheConsent{
			Necessary: true,
			Analytics: true,
			Marketing: true,
		},
		ExplicitLeadConsent: true,
		CachedAt:            time.Now().UTC(),
	}

	// 1. Gravação
	err := rdb.SaveLeadCache(ctx, rec)
	if err != nil {
		t.Fatalf("erro ao salvar lead cache: %v", err)
	}

	// 2. Recuperação
	cached, err := rdb.GetLeadCache(ctx, siteID, subID)
	if err != nil {
		t.Fatalf("erro ao obter lead cache: %v", err)
	}
	if cached == nil {
		t.Fatalf("lead cache esperado não foi encontrado")
	}
	if cached.UserData["email"] != "eduardo@teste.com" {
		t.Errorf("esperado email 'eduardo@teste.com', obtido '%v'", cached.UserData["email"])
	}
	if cached.UserData["phone"] != "54991143857" {
		t.Errorf("esperado phone '54991143857', obtido '%v'", cached.UserData["phone"])
	}
	if !cached.ExplicitLeadConsent {
		t.Errorf("esperado ExplicitLeadConsent=true")
	}

	// 3. Exclusão explícita (LGPD)
	err = rdb.DeleteLeadCache(ctx, siteID, subID)
	if err != nil {
		t.Fatalf("erro ao deletar lead cache: %v", err)
	}

	cachedAfterDel, err := rdb.GetLeadCache(ctx, siteID, subID)
	if err != nil {
		t.Fatalf("erro após delete: %v", err)
	}
	if cachedAfterDel != nil {
		t.Errorf("esperado nil após delete, mas retornou dados")
	}
}

func TestSubmissionDispatchDeduplication(t *testing.T) {
	rdb, ctx := setupTestRedis(t)

	integID := fmt.Sprintf("integ_li_%d", time.Now().UnixNano())
	subID := fmt.Sprintf("sub_dedup_%d", time.Now().UnixNano())

	defer func() {
		key := fmt.Sprintf("dispatch:sub_lock:%s:%s", integID, subID)
		_ = rdb.Client.Del(ctx, key).Err()
	}()

	// 1. Primeira aquisição de lease para a submissão -> Deve adquirir
	acquired, alreadySucceeded, err := rdb.AcquireSubmissionDispatchLease(ctx, integID, subID, 2*time.Second)
	if err != nil {
		t.Fatalf("erro na 1a aquisicao: %v", err)
	}
	if !acquired || alreadySucceeded {
		t.Errorf("esperado acquired=true, alreadySucceeded=false, obtido acquired=%v alreadySucceeded=%v", acquired, alreadySucceeded)
	}

	// 2. Segunda aquisição enquanto processa -> Bloqueada (outro worker ou estágio rodando concorrentemente)
	acquired2, alreadySucceeded2, err := rdb.AcquireSubmissionDispatchLease(ctx, integID, subID, 2*time.Second)
	if err != nil {
		t.Fatalf("erro na 2a aquisicao: %v", err)
	}
	if acquired2 || alreadySucceeded2 {
		t.Errorf("esperado lease bloqueado durante processamento, obtido acquired=%v alreadySucceeded=%v", acquired2, alreadySucceeded2)
	}

	// 3. Marcação de sucesso
	err = rdb.MarkSubmissionDispatchSuccess(ctx, integID, subID)
	if err != nil {
		t.Fatalf("erro marcando sucesso: %v", err)
	}

	// 4. Tentativa de despacho secundário (ex: form_submit_success após form_client_validated)
	acquired3, alreadySucceeded3, err := rdb.AcquireSubmissionDispatchLease(ctx, integID, subID, 2*time.Second)
	if err != nil {
		t.Fatalf("erro na 3a aquisicao: %v", err)
	}
	if acquired3 || !alreadySucceeded3 {
		t.Errorf("esperado acquired=false, alreadySucceeded=true (idempotência), obtido acquired=%v alreadySucceeded=%v", acquired3, alreadySucceeded3)
	}
}
