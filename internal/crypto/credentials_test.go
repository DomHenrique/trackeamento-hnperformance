package crypto

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"testing"
)

func generateRandomKeyHex(t *testing.T) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("failed to read random bytes: %v", err)
	}
	return hex.EncodeToString(b)
}

func TestKeyRing_EncryptDecrypt(t *testing.T) {
	k1 := generateRandomKeyHex(t)
	keysJSON := fmt.Sprintf(`{"k1": "%s"}`, k1)

	kr, err := NewKeyRingFromJSON(keysJSON, "k1")
	if err != nil {
		t.Fatalf("failed to initialize keyring: %v", err)
	}

	siteID := "c56a4180-65aa-42ec-a945-5fd2dec0538d"
	platform := "meta_capi"
	fieldName := "access_token"
	plaintext := "EAABsb123456789SecretTokenABC"

	encrypted, err := kr.EncryptField(plaintext, siteID, platform, fieldName)
	if err != nil {
		t.Fatalf("encryption failed: %v", err)
	}

	if !IsEncrypted(encrypted) {
		t.Fatalf("expected encrypted envelope prefix, got: %s", encrypted)
	}

	kid, err := ExtractKID(encrypted)
	if err != nil {
		t.Fatalf("failed to extract kid: %v", err)
	}
	if kid != "k1" {
		t.Fatalf("expected kid k1, got %s", kid)
	}

	// Decrypt with correct AAD
	decrypted, err := kr.DecryptField(encrypted, siteID, platform, fieldName)
	if err != nil {
		t.Fatalf("decryption failed: %v", err)
	}
	if decrypted != plaintext {
		t.Fatalf("expected %s, got %s", plaintext, decrypted)
	}
}

func TestKeyRing_TamperedAADRejection(t *testing.T) {
	k1 := generateRandomKeyHex(t)
	keysJSON := fmt.Sprintf(`{"k1": "%s"}`, k1)

	kr, err := NewKeyRingFromJSON(keysJSON, "k1")
	if err != nil {
		t.Fatalf("failed to initialize keyring: %v", err)
	}

	site1 := "11111111-1111-1111-1111-111111111111"
	site2 := "22222222-2222-2222-2222-222222222222"
	platform := "meta_capi"
	fieldName := "access_token"
	plaintext := "SuperSecretToken"

	encrypted, err := kr.EncryptField(plaintext, site1, platform, fieldName)
	if err != nil {
		t.Fatalf("encryption failed: %v", err)
	}

	// Try decrypting with different site ID
	_, err = kr.DecryptField(encrypted, site2, platform, fieldName)
	if err == nil {
		t.Fatal("expected authentication error on different site_id, but got nil")
	}

	// Try decrypting with different platform
	_, err = kr.DecryptField(encrypted, site1, "google_ads", fieldName)
	if err == nil {
		t.Fatal("expected authentication error on different platform, but got nil")
	}

	// Try decrypting with different field name
	_, err = kr.DecryptField(encrypted, site1, platform, "pixel_id")
	if err == nil {
		t.Fatal("expected authentication error on different field name, but got nil")
	}
}

func TestKeyRing_KeyRotation(t *testing.T) {
	k1 := generateRandomKeyHex(t)
	k2 := generateRandomKeyHex(t)

	// Encrypt under k1
	krOld, err := NewKeyRingFromJSON(fmt.Sprintf(`{"k1": "%s"}`, k1), "k1")
	if err != nil {
		t.Fatalf("failed to create old keyring: %v", err)
	}

	siteID := "c56a4180-65aa-42ec-a945-5fd2dec0538d"
	platform := "linkedin_capi"
	fieldName := "access_token"
	plaintext := "OldKeyEncryptedToken"

	encryptedK1, err := krOld.EncryptField(plaintext, siteID, platform, fieldName)
	if err != nil {
		t.Fatalf("encryption failed: %v", err)
	}

	// Now introduce k2 as current, keeping k1 in keyring
	krNew, err := NewKeyRingFromJSON(fmt.Sprintf(`{"k1": "%s", "k2": "%s"}`, k1, k2), "k2")
	if err != nil {
		t.Fatalf("failed to create new keyring: %v", err)
	}

	// Decrypt existing k1 ciphertext with the updated keyring
	decryptedK1, err := krNew.DecryptField(encryptedK1, siteID, platform, fieldName)
	if err != nil {
		t.Fatalf("failed to decrypt old ciphertext: %v", err)
	}
	if decryptedK1 != plaintext {
		t.Fatalf("expected %s, got %s", plaintext, decryptedK1)
	}

	// New encryption must use k2
	encryptedK2, err := krNew.EncryptField("NewPayload", siteID, platform, fieldName)
	if err != nil {
		t.Fatalf("failed to encrypt under k2: %v", err)
	}
	kid, err := ExtractKID(encryptedK2)
	if err != nil {
		t.Fatalf("failed to extract kid: %v", err)
	}
	if kid != "k2" {
		t.Fatalf("expected kid k2, got %s", kid)
	}
}

func TestKeyRing_TransparentPlaintextHandling(t *testing.T) {
	k1 := generateRandomKeyHex(t)
	kr, err := NewKeyRingFromJSON(fmt.Sprintf(`{"k1": "%s"}`, k1), "k1")
	if err != nil {
		t.Fatalf("failed to initialize keyring: %v", err)
	}

	plaintext := "legacy_plain_text_token_12345"
	siteID := "11111111-1111-1111-1111-111111111111"

	// Must return plaintext unchanged without error
	val, err := kr.DecryptField(plaintext, siteID, "meta_capi", "access_token")
	if err != nil {
		t.Fatalf("expected transparent pass-through, got error: %v", err)
	}
	if val != plaintext {
		t.Fatalf("expected %s, got %s", plaintext, val)
	}
}

func TestKeyRing_ValidationRules(t *testing.T) {
	k1 := generateRandomKeyHex(t)

	// Colon in kid is prohibited
	_, err := NewKeyRingFromJSON(fmt.Sprintf(`{"k:1": "%s"}`, k1), "k:1")
	if err == nil {
		t.Fatal("expected error on kid with colon, got nil")
	}

	// Empty kid
	_, err = NewKeyRingFromJSON(fmt.Sprintf(`{"": "%s"}`, k1), "")
	if err == nil {
		t.Fatal("expected error on empty kid, got nil")
	}

	// Missing current key
	_, err = NewKeyRingFromJSON(fmt.Sprintf(`{"k1": "%s"}`, k1), "k2")
	if err == nil {
		t.Fatal("expected error when current key not in keyring, got nil")
	}

	// Invalid key length (less than 32 bytes)
	_, err = NewKeyRingFromJSON(`{"k1": "00112233"}`, "k1")
	if err == nil {
		t.Fatal("expected error on short key length, got nil")
	}
}
