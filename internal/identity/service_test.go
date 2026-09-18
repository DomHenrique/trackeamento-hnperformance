package identity

import (
	"testing"
)

func TestNormalizeEmail(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"  joao@Email.Com  ", "joao@email.com"},
		{"Maria.Silva@Dominio.COM.BR", "maria.silva@dominio.com.br"},
	}

	for _, c := range cases {
		result := NormalizeEmail(c.input)
		if result != c.expected {
			t.Errorf("para '%s': esperado '%s', obtido '%s'", c.input, c.expected, result)
		}
	}
}

func TestNormalizePhone(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"(21) 99999-8888", "5521999998888"},
		{"11988887777", "5511988887777"},
		{"+55 21 99999-8888", "5521999998888"},
	}

	for _, c := range cases {
		result := NormalizePhone(c.input)
		if result != c.expected {
			t.Errorf("para '%s': esperado '%s', obtido '%s'", c.input, c.expected, result)
		}
	}
}

func TestHashSHA256(t *testing.T) {
	email := "teste@hnperformancedigital.com.br"
	hash := HashSHA256(email)

	if len(hash) != 64 {
		t.Errorf("esperado hash sha256 de 64 caracteres, obtido %d", len(hash))
	}

	// Hash vazio para entrada vazia
	if HashSHA256("") != "" {
		t.Errorf("esperado hash vazio para string vazia")
	}
}
