package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const (
	SessionCookieName = "_hn_auth_session"
	SessionDuration   = 30 * 24 * time.Hour
)

type User struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

type Service struct {
	pool     *pgxpool.Pool
	sessions sync.Map // token (string) -> SessionInfo
}

type SessionInfo struct {
	User      User
	ExpiresAt time.Time
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{
		pool: pool,
	}
}

// HashPassword gera o hash bcrypt para a senha
func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	return string(bytes), err
}

// CheckPasswordHash compara a senha em texto com o hash
func CheckPasswordHash(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// EnsureAdminUser garante que o usuário admin do .env exista no banco
func (s *Service) EnsureAdminUser(ctx context.Context, username, password string) error {
	if s.pool == nil || strings.TrimSpace(username) == "" || strings.TrimSpace(password) == "" {
		return nil
	}

	username = strings.ToLower(strings.TrimSpace(username))
	var id string
	var existingHash string
	err := s.pool.QueryRow(ctx, "SELECT id::text, password_hash FROM app_users WHERE username = $1", username).Scan(&id, &existingHash)
	if err == nil {
		// Usuário já existe. Se a senha tiver sido alterada no .env, sincroniza
		if !CheckPasswordHash(password, existingHash) {
			newHash, errHash := HashPassword(password)
			if errHash == nil {
				_, _ = s.pool.Exec(ctx, "UPDATE app_users SET password_hash = $1, updated_at = now() WHERE id = $2", newHash, id)
				fmt.Printf("[Auth] Senha do admin '%s' sincronizada com sucesso.\n", username)
			}
		}
		return nil
	}

	// Insere o admin inicial
	newHash, errHash := HashPassword(password)
	if errHash != nil {
		return errHash
	}

	_, err = s.pool.Exec(ctx, "INSERT INTO app_users (username, password_hash, role) VALUES ($1, $2, 'admin')", username, newHash)
	if err == nil {
		fmt.Printf("[Auth] Usuário administrador mestre '%s' criado com sucesso.\n", username)
	}
	return err
}

func generateSessionToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// HandleLogin autentica o usuário e emite o cookie de sessão
func (s *Service) HandleLogin(c *fiber.Ctx) error {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "dados invalidos"})
	}

	username := strings.ToLower(strings.TrimSpace(req.Username))
	password := req.Password

	if s.pool == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "banco de dados indisponivel"})
	}

	var user User
	var hash string
	err := s.pool.QueryRow(c.Context(), "SELECT id::text, username, password_hash, role, created_at FROM app_users WHERE username = $1", username).Scan(
		&user.ID, &user.Username, &hash, &user.Role, &user.CreatedAt,
	)
	if err != nil || !CheckPasswordHash(password, hash) {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "usuario ou senha incorretos"})
	}

	// Cria sessão
	token := generateSessionToken()
	s.sessions.Store(token, SessionInfo{
		User:      user,
		ExpiresAt: time.Now().Add(SessionDuration),
	})

	c.Cookie(&fiber.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		MaxAge:   int(SessionDuration.Seconds()),
		Path:     "/",
		SameSite: "Lax",
		Secure:   true,
		HTTPOnly: true,
	})

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"status": "authenticated",
		"user":   user,
		"token":  token,
	})
}

// HandleLogout encerra a sessão ativa
func (s *Service) HandleLogout(c *fiber.Ctx) error {
	token := c.Cookies(SessionCookieName)
	if token == "" {
		token = strings.TrimPrefix(c.Get("Authorization"), "Bearer ")
	}
	if token != "" {
		s.sessions.Delete(token)
	}

	c.Cookie(&fiber.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		MaxAge:   -1,
		Path:     "/",
		SameSite: "Lax",
		Secure:   true,
		HTTPOnly: true,
	})

	return c.Status(fiber.StatusOK).JSON(fiber.Map{"status": "logged_out"})
}

// HandleMe retorna as informações do usuário autenticado
func (s *Service) HandleMe(c *fiber.Ctx) error {
	user, ok := s.GetUserFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"authenticated": false})
	}
	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"authenticated": true,
		"user":          user,
	})
}

// HandleCreateUser permite que um admin crie novos operadores
func (s *Service) HandleCreateUser(c *fiber.Ctx) error {
	currentUser, ok := s.GetUserFromCtx(c)
	if !ok || currentUser.Role != "admin" {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "apenas administradores podem cadastrar usuarios"})
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "dados invalidos"})
	}

	username := strings.ToLower(strings.TrimSpace(req.Username))
	if len(username) < 3 || len(req.Password) < 6 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "username minimo 3 caracteres e senha minimo 6 caracteres"})
	}

	role := strings.ToLower(strings.TrimSpace(req.Role))
	if role != "admin" && role != "operator" {
		role = "operator"
	}

	hash, err := HashPassword(req.Password)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "falha ao processar senha"})
	}

	var newID string
	err = s.pool.QueryRow(c.Context(), "INSERT INTO app_users (username, password_hash, role) VALUES ($1, $2, $3) RETURNING id::text", username, hash, role).Scan(&newID)
	if err != nil {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "usuario ja existe ou erro no banco"})
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"status": "created",
		"user": fiber.Map{
			"id":       newID,
			"username": username,
			"role":     role,
		},
	})
}

// GetUserFromCtx extrai o usuário a partir do cookie ou header Authorization
func (s *Service) GetUserFromCtx(c *fiber.Ctx) (User, bool) {
	token := c.Cookies(SessionCookieName)
	if token == "" {
		token = strings.TrimPrefix(c.Get("Authorization"), "Bearer ")
	}
	if token == "" {
		return User{}, false
	}

	val, ok := s.sessions.Load(token)
	if !ok {
		return User{}, false
	}

	sess := val.(SessionInfo)
	if time.Now().After(sess.ExpiresAt) {
		s.sessions.Delete(token)
		return User{}, false
	}

	return sess.User, true
}

// RequireAuth middleware que bloqueia requisições não autenticadas
func (s *Service) RequireAuth() fiber.Handler {
	return func(c *fiber.Ctx) error {
		_, ok := s.GetUserFromCtx(c)
		if !ok {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "autenticacao necessaria"})
		}
		return c.Next()
	}
}
