package crypto

import (
	"fmt"
	"testing"
)

func TestMigrateCredentialMap_PlaintextToEncrypted(t *testing.T) {
	k1 := generateRandomKeyHex(t)
	kr, err := NewKeyRingFromJSON(fmt.Sprintf(`{"k1": "%s"}`, k1), "k1")
	if err != nil {
		t.Fatalf("falha ao criar keyring: %v", err)
	}

	siteID := "c56a4180-65aa-42ec-a945-5fd2dec0538d"
	platform := "meta_capi"

	creds := map[string]string{
		"pixel_id":     "1234567890",
		"access_token": "EAABplaintextToken123",
	}

	newCreds, updated, nEnc, nRekey, err := MigrateCredentialMap(creds, siteID, platform, kr, false)
	if err != nil {
		t.Fatalf("erro ao migrar: %v", err)
	}

	if !updated {
		t.Fatal("esperava updated=true")
	}
	if nEnc != 1 || nRekey != 0 {
		t.Fatalf("esperava 1 enc e 0 rekey, obteve nEnc=%d, nRekey=%d", nEnc, nRekey)
	}

	// pixel_id permanece inalterado
	if newCreds["pixel_id"] != "1234567890" {
		t.Errorf("pixel_id foi alterado indevidamente: %s", newCreds["pixel_id"])
	}

	// access_token foi cifrado com k1
	encToken := newCreds["access_token"]
	if !IsEncrypted(encToken) {
		t.Fatalf("access_token não foi cifrado: %s", encToken)
	}
	kid, _ := ExtractKID(encToken)
	if kid != "k1" {
		t.Fatalf("esperava kid k1, obteve %s", kid)
	}

	// Decifra e confere valor original
	dec, err := kr.DecryptField(encToken, siteID, platform, "access_token")
	if err != nil {
		t.Fatalf("falha ao decifrar: %v", err)
	}
	if dec != "EAABplaintextToken123" {
		t.Fatalf("esperava texto original, obteve: %s", dec)
	}

	// Idempotência: rodar novamente não deve alterar nada
	newCreds2, updated2, nEnc2, nRekey2, err := MigrateCredentialMap(newCreds, siteID, platform, kr, false)
	if err != nil {
		t.Fatalf("erro na segunda rodada: %v", err)
	}
	if updated2 || nEnc2 != 0 || nRekey2 != 0 {
		t.Fatalf("esperava idempotência (updated=false), obteve updated=%v", updated2)
	}
	if newCreds2["access_token"] != encToken {
		t.Errorf("valor de ciphertext mudou inesperadamente")
	}
}

func TestMigrateCredentialMap_RekeyRotation(t *testing.T) {
	k1 := generateRandomKeyHex(t)
	k2 := generateRandomKeyHex(t)

	krOld, _ := NewKeyRingFromJSON(fmt.Sprintf(`{"k1": "%s"}`, k1), "k1")
	siteID := "c56a4180-65aa-42ec-a945-5fd2dec0538d"
	platform := "meta_capi"
	rawToken := "EAABsecretTokenK1"

	encOld, err := krOld.EncryptField(rawToken, siteID, platform, "access_token")
	if err != nil {
		t.Fatalf("falha ao cifrar sob k1: %v", err)
	}

	// Cria novo keyring onde k2 é a chave ativa e k1 ainda está presente para leitura
	krNew, err := NewKeyRingFromJSON(fmt.Sprintf(`{"k1": "%s", "k2": "%s"}`, k1, k2), "k2")
	if err != nil {
		t.Fatalf("falha ao criar novo keyring: %v", err)
	}

	creds := map[string]string{
		"access_token": encOld,
	}

	// 1. Sem a flag rekey, não deve fazer nada se já estiver cifrado (mesmo com kid antigo)
	_, updatedNoRekey, _, _, _ := MigrateCredentialMap(creds, siteID, platform, krNew, false)
	if updatedNoRekey {
		t.Fatal("esperava updated=false quando rekey=false")
	}

	// 2. Com a flag rekey=true, deve decifrar de k1 e recifrar para k2
	rekeyedMap, updated, nEnc, nRekey, err := MigrateCredentialMap(creds, siteID, platform, krNew, true)
	if err != nil {
		t.Fatalf("falha ao executar rekey: %v", err)
	}
	if !updated {
		t.Fatal("esperava updated=true durante rekey")
	}
	if nRekey != 1 || nEnc != 0 {
		t.Fatalf("esperava nRekey=1, obteve nRekey=%d, nEnc=%d", nRekey, nEnc)
	}

	newEnc := rekeyedMap["access_token"]
	kid, err := ExtractKID(newEnc)
	if err != nil {
		t.Fatalf("falha ao extrair kid do rekey: %v", err)
	}
	if kid != "k2" {
		t.Fatalf("esperava kid 'k2', obteve %s", kid)
	}

	// Confere se decifra com k2
	decrypted, err := krNew.DecryptField(newEnc, siteID, platform, "access_token")
	if err != nil {
		t.Fatalf("falha ao decifrar token após rekey: %v", err)
	}
	if decrypted != rawToken {
		t.Fatalf("esperava token %s, obteve %s", rawToken, decrypted)
	}
}
