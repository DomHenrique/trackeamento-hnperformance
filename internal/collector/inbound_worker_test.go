package collector_test

import (
	"context"
	"testing"
	"time"

	"tracking-engine/internal/collector"
	"tracking-engine/internal/inbound"
)

func TestInboundWorker_LifecycleAndNotify(t *testing.T) {
	// Testa ciclo de vida do worker sem pânico mesmo com dependências nulas
	worker := collector.NewInboundWorker(nil, nil, func(ctx context.Context, siteID string, norm *inbound.NormalizedCRMEvent, inboundEventID string) error {
		return nil
	})

	// Notificação não bloqueia
	for i := 0; i < 150; i++ {
		worker.Notify()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	worker.Start(ctx)

	// Pequeno delay
	time.Sleep(20 * time.Millisecond)

	worker.Stop()
}
