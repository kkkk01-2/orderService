package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)
// credentials представляет тело запроса для /register и /login
type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}
// sessionKey возвращает ключ для хранения сессии в Redis
func sessionKey(token string) string { return "session:" + token }
// normalizeEmail приводит email к нижнему регистру и убирает пробелы по краям
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
// bearerToken возвращает токен из заголовка Authorization, если он есть
func bearerToken(r *http.Request) string {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return token
}
// register создает нового пользователя, хэширует пароль и сохраняет в базе
func (a *App) register(w http.ResponseWriter, r *http.Request) {
	var req credentials
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	email := normalizeEmail(req.Email)
	// bcrypt принимает пароли не длиннее 72 байт
	if !strings.Contains(email, "@") || len(req.Password) < 8 || len(req.Password) > 72 {
		writeErr(w, http.StatusUnprocessableEntity, "invalid email or password (8-72 chars)")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		a.fail(w, err)
		return
	}
	id, err := a.store.CreateUser(r.Context(), email, string(hash))
	if errors.Is(err, ErrEmailTaken) {
		writeErr(w, http.StatusConflict, "email already taken")
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "email": email})
}

// login проверяет email и хеш, пароль, создает сессию в Redis и отправляет json-токен клиенту
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	var req credentials
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}

	u, err := a.store.GetUserByEmail(r.Context(), normalizeEmail(req.Email))
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusUnauthorized, "invalid credentials") // не раскрываем, что именно неверно
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)) != nil {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	token := uuid.NewString()
	if err := a.rdb.Set(r.Context(), sessionKey(token), u.ID, a.sessionTTL).Err(); err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

// logout удаляет сессию из Redis, если токен был передан в заголовке Authorization
func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if token := bearerToken(r); token != "" {
		if err := a.rdb.Del(r.Context(), sessionKey(token)).Err(); err != nil {
			a.fail(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// me работает за auth-middleware, userID уже лежит в контексте
func (a *App) me(w http.ResponseWriter, r *http.Request) {
	uid := r.Context().Value(ctxKey{}).(int64)
	u, err := a.store.GetUserByID(r.Context(), uid)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": u.ID, "email": u.Email})
}