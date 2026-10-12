package inbound

import (
	"testing"
	"time"
)

func TestParseMoneyToCents(t *testing.T) {
	tests := []struct {
		name         string
		raw          interface{}
		currency     string
		wantCents    int64
		wantCurrency string
		wantErr      bool
	}{
		{
			name:         "Float standard BRL",
			raw:          149.90,
			currency:     "BRL",
			wantCents:    14990,
			wantCurrency: "BRL",
		},
		{
			name:         "String pt-BR com separador milhar",
			raw:          "1.450,50",
			currency:     "BRL",
			wantCents:    145050,
			wantCurrency: "BRL",
		},
		{
			name:         "String en-US com separador milhar",
			raw:          "1,450.50",
			currency:     "USD",
			wantCents:    145050,
			wantCurrency: "USD",
		},
		{
			name:         "String apenas vírgula",
			raw:          "99,99",
			currency:     "BRL",
			wantCents:    9999,
			wantCurrency: "BRL",
		},
		{
			name:         "String com prefixo R$",
			raw:          "R$ 250,00",
			currency:     "BRL",
			wantCents:    25000,
			wantCurrency: "BRL",
		},
		{
			name:         "Moeda sem casas decimais JPY",
			raw:          1500.0,
			currency:     "JPY",
			wantCents:    1500,
			wantCurrency: "JPY",
		},
		{
			name:         "Moeda vazia assume BRL",
			raw:          "50.00",
			currency:     "",
			wantCents:    5000,
			wantCurrency: "BRL",
		},
		{
			name:         "Nil value",
			raw:          nil,
			currency:     "BRL",
			wantCents:    0,
			wantCurrency: "BRL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cents, cur, err := ParseMoneyToCents(tt.raw, tt.currency)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseMoneyToCents() error = %v, wantErr %v", err, tt.wantErr)
			}
			if cents != tt.wantCents {
				t.Errorf("ParseMoneyToCents() cents = %v, want %v", cents, tt.wantCents)
			}
			if cur != tt.wantCurrency {
				t.Errorf("ParseMoneyToCents() currency = %v, want %v", cur, tt.wantCurrency)
			}
		})
	}
}

func TestComputeDedupKey(t *testing.T) {
	siteID := "site-123"
	body := []byte(`{"event":"test","id":"456"}`)

	// 1. ProviderEventID tem precedência máxima
	normWithProv := &NormalizedCRMEvent{
		ProviderEventID: "evt_stripe_999",
		OrderID:         "PED-001",
		EventName:       "purchase",
	}
	key1 := ComputeDedupKey(siteID, normWithProv, body)
	if key1 != "evt_stripe_999" {
		t.Errorf("expected provider event id, got %s", key1)
	}

	// 2. OrderID + EventName
	normWithOrder := &NormalizedCRMEvent{
		OrderID:   "PED-001",
		EventName: "payment_approved",
	}
	key2 := ComputeDedupKey(siteID, normWithOrder, body)
	if len(key2) != 64 {
		t.Errorf("expected 64 char sha256 hash, got %d chars (%s)", len(key2), key2)
	}

	// Reembolso com mesmo OrderID gera chave diferente de purchase!
	normRefund := &NormalizedCRMEvent{
		OrderID:   "PED-001",
		EventName: "refund",
	}
	keyRefund := ComputeDedupKey(siteID, normRefund, body)
	if key2 == keyRefund {
		t.Errorf("expected different dedup keys for payment_approved and refund, got identical")
	}

	// 3. Fallback body hash
	normEmpty := &NormalizedCRMEvent{}
	key3 := ComputeDedupKey(siteID, normEmpty, body)
	if len(key3) != 64 {
		t.Errorf("expected 64 char sha256 hash for raw body, got %d chars", len(key3))
	}
}

func TestValidateOccurredAt(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

	// Zero -> now
	tZero, isStale, err := ValidateOccurredAt(time.Time{}, now)
	if err != nil || isStale || !tZero.Equal(now) {
		t.Errorf("expected now for zero time, got %v, isStale=%v, err=%v", tZero, isStale, err)
	}

	// Venda há 2 horas atrás -> válida e não stale
	twoHoursAgo := now.Add(-2 * time.Hour)
	tValid, isStale, err := ValidateOccurredAt(twoHoursAgo, now)
	if err != nil || isStale || !tValid.Equal(twoHoursAgo) {
		t.Errorf("expected valid non-stale for 2 hours ago, got isStale=%v, err=%v", isStale, err)
	}

	// Venda há 10 dias atrás -> válida mas stale
	tenDaysAgo := now.Add(-10 * 24 * time.Hour)
	tStale, isStale, err := ValidateOccurredAt(tenDaysAgo, now)
	if err != nil || !isStale || !tStale.Equal(tenDaysAgo) {
		t.Errorf("expected isStale=true for 10 days ago, got isStale=%v, err=%v", isStale, err)
	}

	// Data futura superior a 5 min -> erro
	tenMinFuture := now.Add(10 * time.Minute)
	_, _, err = ValidateOccurredAt(tenMinFuture, now)
	if err == nil {
		t.Errorf("expected error for future timestamp, got nil")
	}
}

func TestDeterministicPurchaseEventID(t *testing.T) {
	id := DeterministicPurchaseEventID("PED-9874 / 2026")
	expected := "order_PED-9874___2026"
	if id != expected {
		t.Errorf("expected %s, got %s", expected, id)
	}
}
