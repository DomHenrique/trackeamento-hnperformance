package collector

import (
	"context"
	"testing"
	"time"

	"tracking-engine/internal/config"
)

func TestCollector_SiteKeyCacheTTL(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	h := NewHandler(cfg, nil, nil, nil)

	testKey := "hn_live_test_ttl_key_123"
	meta := &SiteMetadata{
		ID:    "site-123",
		KeyID: "key-uuid-abc",
	}

	// 1. Chave não expirada (TTL ativo)
	h.siteKeys.Store(testKey, &CachedSiteKey{
		KeyID:     meta.KeyID,
		Metadata:  meta,
		ExpiresAt: time.Now().Add(5 * time.Minute),
	})

	gotMeta, valid := h.validateSiteKey(context.Background(), testKey)
	if !valid || gotMeta == nil || gotMeta.ID != "site-123" {
		t.Fatalf("esperava chave válida com TTL ativo, obtido valid=%v", valid)
	}

	// 2. Chave com TTL expirado no passado
	expiredKey := "hn_live_expired_key_456"
	h.siteKeys.Store(expiredKey, &CachedSiteKey{
		KeyID:     "key-uuid-def",
		Metadata:  meta,
		ExpiresAt: time.Now().Add(-1 * time.Second),
	})

	_, validExpired := h.validateSiteKey(context.Background(), expiredKey)
	if validExpired {
		t.Fatalf("esperava que chave com TTL expirado fosse descartada e considerada inválida")
	}

	// Verifica se foi removida da memória
	if _, found := h.siteKeys.Load(expiredKey); found {
		t.Errorf("esperava chave expirada removida do mapa de memória")
	}
}

func TestCollector_CacheInvalidationMethods(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	h := NewHandler(cfg, nil, nil, nil)

	key1 := "hn_live_k1"
	key2 := "hn_live_k2"
	key3 := "hn_live_k3"

	h.siteKeys.Store(key1, &CachedSiteKey{
		KeyID:     "key-1",
		Metadata:  &SiteMetadata{ID: "site-A", KeyID: "key-1"},
		ExpiresAt: time.Now().Add(5 * time.Minute),
	})
	h.siteKeys.Store(key2, &CachedSiteKey{
		KeyID:     "key-2",
		Metadata:  &SiteMetadata{ID: "site-A", KeyID: "key-2"},
		ExpiresAt: time.Now().Add(5 * time.Minute),
	})
	h.siteKeys.Store(key3, &CachedSiteKey{
		KeyID:     "key-3",
		Metadata:  &SiteMetadata{ID: "site-B", KeyID: "key-3"},
		ExpiresAt: time.Now().Add(5 * time.Minute),
	})

	// 1. Invalida por KeyID específico
	h.InvalidateKeyByID("key-1")
	if _, found := h.siteKeys.Load(key1); found {
		t.Errorf("esperava key1 invalidada por InvalidateKeyByID")
	}
	if _, found := h.siteKeys.Load(key2); !found {
		t.Errorf("key2 não deveria ter sido removida")
	}

	// 2. Invalida por SiteID
	h.InvalidateSiteCache("site-A")
	if _, found := h.siteKeys.Load(key2); found {
		t.Errorf("esperava key2 invalidada por InvalidateSiteCache")
	}
	if _, found := h.siteKeys.Load(key3); !found {
		t.Errorf("key3 (site-B) deveria permanecer intacta")
	}

	// 3. Clear total (reconexão)
	h.ClearSiteKeysCache()
	if _, found := h.siteKeys.Load(key3); found {
		t.Errorf("esperava key3 removida após ClearSiteKeysCache")
	}
}

func TestCollector_RecordKeyUsageNoBlock(t *testing.T) {
	cfg := &config.Config{Env: "test"}
	h := NewHandler(cfg, nil, nil, nil)

	// Não deve dar panic quando redis ou pg forem nil
	h.RecordKeyUsage("hn_live_no_panic_key")
	h.RecordKeyUsage("")
}
