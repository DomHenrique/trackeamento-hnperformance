package auth

import (
	"testing"
)

func TestHashAndCheckPassword(t *testing.T) {
	pwd := "MinhaSenhaSuperSegura123!"
	hash, err := HashPassword(pwd)
	if err != nil {
		t.Fatalf("Erro inesperado ao gerar hash: %v", err)
	}

	if !CheckPasswordHash(pwd, hash) {
		t.Errorf("Esperava que a senha original conferisse com o hash gerado")
	}

	if CheckPasswordHash("SenhaErrada", hash) {
		t.Errorf("Senha errada não deveria validar contra o hash")
	}
}

func TestGenerateSessionToken(t *testing.T) {
	t1 := generateSessionToken()
	t2 := generateSessionToken()

	if len(t1) != 64 {
		t.Errorf("Esperava token de 64 caracteres hex (32 bytes), obtido %d", len(t1))
	}
	if t1 == t2 {
		t.Errorf("Dois tokens gerados sequencialmente não devem ser iguais")
	}
}
