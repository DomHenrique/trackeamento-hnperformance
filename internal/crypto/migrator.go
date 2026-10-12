package crypto

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// IsSensitiveCredentialKey determina se a chave representa um segredo que deve ser cifrado.
func IsSensitiveCredentialKey(k string) bool {
	lower := strings.ToLower(k)
	return strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || strings.Contains(lower, "key")
}

// RekeyStats consolida as métricas da operação de migração.
type RekeyStats struct {
	TotalProcessed  int
	UpdatedRecords  int
	FieldsEncrypted int
	FieldsRekeyed   int
}

// MigrateCredentialMap migra os campos sensíveis de um mapa de credenciais.
// Se rekey=false: cifra campos sensíveis em texto plano.
// Se rekey=true: cifra campos sensíveis em texto plano E recifra campos com kid divergente do corrente.
func MigrateCredentialMap(creds map[string]string, siteID, platform string, kr *KeyRing, rekey bool) (map[string]string, bool, int, int, error) {
	if kr == nil {
		return nil, false, 0, 0, fmt.Errorf("migrator: keyring não pode ser nulo")
	}

	updated := false
	fieldsEncrypted := 0
	fieldsRekeyed := 0
	newMap := make(map[string]string, len(creds))

	for k, v := range creds {
		trimmed := strings.TrimSpace(v)
		if !IsSensitiveCredentialKey(k) || trimmed == "" {
			newMap[k] = trimmed
			continue
		}

		if !IsEncrypted(trimmed) {
			// Campo em texto plano legado: cifra com chave atual e AAD
			enc, err := kr.EncryptField(trimmed, siteID, platform, k)
			if err != nil {
				return nil, false, 0, 0, fmt.Errorf("erro ao cifrar campo %s (site %s): %w", k, siteID, err)
			}
			newMap[k] = enc
			updated = true
			fieldsEncrypted++
		} else if rekey {
			// Já cifrado. Confere o kid atual.
			kid, err := ExtractKID(trimmed)
			if err != nil {
				return nil, false, 0, 0, fmt.Errorf("envelope inválido no campo %s (site %s): %w", k, siteID, err)
			}

			if kid != kr.CurrentKID() {
				// Decifra com a chave antiga
				dec, err := kr.DecryptField(trimmed, siteID, platform, k)
				if err != nil {
					return nil, false, 0, 0, fmt.Errorf("falha ao decifrar com kid antigo %s no campo %s (site %s): %w", kid, k, siteID, err)
				}
				// Recifra com a chave atual
				newEnc, err := kr.EncryptField(dec, siteID, platform, k)
				if err != nil {
					return nil, false, 0, 0, fmt.Errorf("falha ao recifrar campo %s (site %s): %w", k, siteID, err)
				}
				newMap[k] = newEnc
				updated = true
				fieldsRekeyed++
			} else {
				newMap[k] = trimmed
			}
		} else {
			newMap[k] = trimmed
		}
	}

	return newMap, updated, fieldsEncrypted, fieldsRekeyed, nil
}

// RunRekeyMigration executa a varredura da tabela site_integrations e atualiza registros que precisam de cifragem ou rotação.
func RunRekeyMigration(ctx context.Context, pool *pgxpool.Pool, kr *KeyRing, rekey bool, dryRun bool) (*RekeyStats, error) {
	if pool == nil {
		return nil, fmt.Errorf("migrator: pool do banco de dados não pode ser nulo")
	}

	rows, err := pool.Query(ctx, `
		SELECT site_id::text, platform, credentials 
		FROM site_integrations
	`)
	if err != nil {
		return nil, fmt.Errorf("migrator: falha ao consultar site_integrations: %w", err)
	}
	defer rows.Close()

	type recordToUpdate struct {
		siteID   string
		platform string
		creds    map[string]string
	}

	var toUpdate []recordToUpdate
	stats := &RekeyStats{}

	for rows.Next() {
		var siteID string
		var platform string
		var credsJSON []byte

		if err := rows.Scan(&siteID, &platform, &credsJSON); err != nil {
			return nil, fmt.Errorf("migrator: erro no scan: %w", err)
		}

		stats.TotalProcessed++

		var creds map[string]string
		if len(credsJSON) > 0 {
			if err := json.Unmarshal(credsJSON, &creds); err != nil {
				log.Printf("[Migrator] Erro ao deserializar json do site %s plataforma %s: %v", siteID, platform, err)
				continue
			}
		} else {
			creds = make(map[string]string)
		}

		newCreds, updated, nEnc, nRekey, err := MigrateCredentialMap(creds, siteID, platform, kr, rekey)
		if err != nil {
			return nil, fmt.Errorf("migrator: falha no processamento do site %s plataforma %s: %w", siteID, platform, err)
		}

		if updated {
			stats.UpdatedRecords++
			stats.FieldsEncrypted += nEnc
			stats.FieldsRekeyed += nRekey
			toUpdate = append(toUpdate, recordToUpdate{
				siteID:   siteID,
				platform: platform,
				creds:    newCreds,
			})
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("migrator: erro ao iterar linhas: %w", err)
	}

	if dryRun {
		log.Printf("[Migrator] Modo Dry-Run ativado. %d registros seriam atualizados.", len(toUpdate))
		return stats, nil
	}

	// Persiste as alterações no banco de dados de forma transacional
	for _, rec := range toUpdate {
		updatedJSON, err := json.Marshal(rec.creds)
		if err != nil {
			return nil, fmt.Errorf("migrator: erro ao serializar credenciais atualizadas: %w", err)
		}

		_, err = pool.Exec(ctx, `
			UPDATE site_integrations 
			SET credentials = $1::jsonb, updated_at = now()
			WHERE site_id = $2::uuid AND platform = $3
		`, updatedJSON, rec.siteID, rec.platform)
		if err != nil {
			return nil, fmt.Errorf("migrator: falha ao atualizar registro site %s plataforma %s: %w", rec.siteID, rec.platform, err)
		}
	}

	return stats, nil
}
