package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"tracking-engine/internal/collector"
	"tracking-engine/internal/config"
	"tracking-engine/internal/dispatcher"
	"tracking-engine/internal/identity"
	"tracking-engine/internal/storage"
)

func main() {
	siteIDFlag := flag.String("site-id", "", "UUID do Site no Trackeamento HN (obrigatório)")
	submissionIDFlag := flag.String("submission-id", "", "ID da submissão do formulário a reprocessar")
	emailFlag := flag.String("email", "", "E-mail em texto claro para normalização e hash")
	phoneFlag := flag.String("phone", "", "Telefone em texto claro (com DDI e DDD)")
	nameFlag := flag.String("name", "", "Nome do lead")
	eventNameFlag := flag.String("event-name", "form_submit", "Nome do evento de conversão")
	lifecycleStateFlag := flag.String("lifecycle-state", "form_submit_success", "Estado do ciclo de vida do formulário")
	occurredAtFlag := flag.String("occurred-at", "", "Data/hora ISO-8601 do evento original (ex: 2026-10-18T14:30:00Z). Padrão: agora")
	dryRunFlag := flag.Bool("dry-run", false, "Simula o reprocessamento sem enfileirar nem gravar dados")
	consentFlag := flag.Bool("explicit-consent", true, "Confirmação de consentimento explícito do lead (LGPD)")
	queueDirectFlag := flag.Bool("queue", true, "Enfileira no stream Redis para despacho assíncrono pelos workers")

	flag.Parse()

	log.Println("==================================================================")
	log.Println(" Trackeamento HN - Utilitário de Reprocessamento de Leads")
	log.Println("==================================================================")

	if *siteIDFlag == "" {
		log.Fatalf("[ERRO] Parâmetro --site-id é obrigatório.")
	}

	siteUUID, err := uuid.Parse(*siteIDFlag)
	if err != nil {
		log.Fatalf("[ERRO] --site-id inválido: %v", err)
	}

	if *emailFlag == "" && *phoneFlag == "" {
		log.Fatalf("[ERRO] Ao menos um identificador (--email ou --phone) deve ser fornecido para despacho em plataformas de anúncios.")
	}

	if !*consentFlag {
		log.Fatalf("[ERRO LGPD] Não é permitido reprocessar dados de contato de leads sem consentimento explícito (--explicit-consent=true).")
	}

	var eventTime time.Time
	if *occurredAtFlag != "" {
		parsedTime, err := time.Parse(time.RFC3339, *occurredAtFlag)
		if err != nil {
			log.Fatalf("[ERRO] Formato de data inválido para --occurred-at (use RFC3339, ex: 2026-10-18T14:30:00Z): %v", err)
		}
		eventTime = parsedTime.UTC()
	} else {
		eventTime = time.Now().UTC()
	}

	// Validação das janelas de aceitação retroativa das plataformas
	age := time.Since(eventTime)
	daysOld := age.Hours() / 24.0

	log.Printf("Evento datado de: %s (idade: %.1f dias)", eventTime.Format(time.RFC3339), daysOld)
	log.Println("Verificação de Janelas de Aceitação Retroativa:")

	if daysOld > 7.0 {
		log.Printf("  [AVISO] Meta CAPI: Limite é de 7 dias retroativos. Eventos com %.1f dias podem ser descartados pela Meta.", daysOld)
	} else {
		log.Printf("  [OK] Meta CAPI: Dentro da janela de 7 dias (%.1f dias).", daysOld)
	}

	if daysOld > 90.0 {
		log.Printf("  [AVISO] Google Ads / Data Manager: Limite é de 90 dias retroativos. Eventos com %.1f dias podem ser descartados pelo Google.", daysOld)
	} else {
		log.Printf("  [OK] Google Ads: Dentro da janela de 90 dias (%.1f dias).", daysOld)
	}

	if daysOld > 90.0 {
		log.Printf("  [AVISO] LinkedIn CAPI: Limite é de 90 dias retroativos. Eventos com %.1f dias podem ser descartados pelo LinkedIn.", daysOld)
	} else {
		log.Printf("  [OK] LinkedIn CAPI: Dentro da janela de 90 dias (%.1f dias).", daysOld)
	}

	// Normalização e hashes de conformidade
	normEmail := identity.NormalizeEmail(*emailFlag)
	hashEmail := identity.HashSHA256(normEmail)

	normPhone := identity.NormalizePhone(*phoneFlag)
	hashPhone := identity.HashSHA256(normPhone)

	subID := *submissionIDFlag
	if subID == "" {
		subID = fmt.Sprintf("sub_retro_%d", time.Now().UnixNano())
		log.Printf("Gerado submission_id determinístico: %s", subID)
	}

	userData := make(map[string]interface{})
	if normEmail != "" {
		userData["email"] = normEmail
		userData["email_hash"] = hashEmail
	}
	if normPhone != "" {
		userData["phone"] = normPhone
		userData["phone_hash"] = hashPhone
	}
	if *nameFlag != "" {
		userData["name"] = strings.TrimSpace(*nameFlag)
	}

	evPayload := &collector.EventPayload{
		EventID:             fmt.Sprintf("ev_retro_%d", time.Now().UnixNano()),
		SiteID:              siteUUID.String(),
		SubmissionID:        subID,
		EventName:           *eventNameFlag,
		FormLifecycleState:  *lifecycleStateFlag,
		UserData:            userData,
		Consent:             collector.ConsentState{Analytics: true, Marketing: true},
		ExplicitLeadConsent: true,
		EventTime:           eventTime,
		CreatedAt:           time.Now().UTC(),
	}

	cfg := config.Load()
	rdb, err := storage.NewRedis(cfg)
	if err != nil {
		log.Fatalf("[ERRO] Falha ao conectar ao Redis: %v", err)
	}
	defer rdb.Close()

	ctx := context.Background()

	// 1. Armazena no LeadCacheRecord temporário no Redis
	leadRec := &storage.LeadCacheRecord{
		SubmissionID: subID,
		SiteID:       siteUUID.String(),
		UserData:     userData,
		Consent: storage.LeadCacheConsent{
			Analytics: true,
			Marketing: true,
		},
		ExplicitLeadConsent: true,
		CachedAt:            time.Now().UTC(),
	}

	if *dryRunFlag {
		log.Println("\n[DRY RUN] Operação simulada com sucesso.")
		log.Printf("Lead pronto para reprocessamento: Site=%s, SubID=%s, EmailHash=%s, PhoneHash=%s",
			siteUUID.String(), subID, hashEmail, hashPhone)
		return
	}

	if err := rdb.SaveLeadCache(ctx, leadRec); err != nil {
		log.Fatalf("[ERRO] Falha ao gravar LeadCache no Redis: %v", err)
	}
	log.Printf("[SUCESSO] Cache temporário do lead registrado no Redis (TTL: 15 min).")

	// 2. Enfileira o evento para despacho no stream do Redis
	if *queueDirectFlag {
		dMsg := dispatcher.DispatchMessage{
			Event: evPayload,
		}
		pBytes, err := json.Marshal(dMsg)
		if err != nil {
			log.Fatalf("[ERRO] Falha ao serializar mensagem de despacho: %v", err)
		}

		streamKey := cfg.RedisStreamDispatch
		if streamKey == "" {
			streamKey = "stream:events:dispatch"
		}

		xID, err := rdb.Client.XAdd(ctx, &redis.XAddArgs{
			Stream: streamKey,
			Values: map[string]interface{}{
				"payload": string(pBytes),
			},
		}).Result()

		if err != nil {
			log.Fatalf("[ERRO] Falha ao enviar para o stream do Redis '%s': %v", streamKey, err)
		}

		log.Printf("[SUCESSO] Evento de reprocessamento publicado no stream '%s' com ID: %s", streamKey, xID)
		log.Println("Os workers do dispatcher consumirão o evento e despacharão para LinkedIn, Meta e Google Ads.")
	}

	log.Println("==================================================================")
	log.Println(" Reprocessamento concluído com êxito!")
	log.Println("==================================================================")
}
