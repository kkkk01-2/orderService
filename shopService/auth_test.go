package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func TestRegister_Success_PasswordIsHashed(t *testing.T) {
	app, st, _ := setup(t)
	isBcryptOfPassword := mock.MatchedBy(func(hash string) bool {
		return hash != "secret123" &&
			bcrypt.CompareHashAndPassword([]byte(hash), []byte("secret123")) == nil
	})
	st.On("CreateUser", mock.Anything, "a@b.com", isBcryptOfPassword).Return(int64(1), nil)

	rec := do(app, "", "POST", "/register", `{"email":" A@B.com ","password":"secret123"}`)

	require.Equal(t, http.StatusCreated, rec.Code)
	st.AssertExpectations(t)
}

func TestRegister_EmailTaken(t *testing.T) {
	app, st, _ := setup(t)
	st.On("CreateUser", mock.Anything, mock.Anything, mock.Anything).Return(int64(0), ErrEmailTaken)

	rec := do(app, "", "POST", "/register", `{"email":"a@b.com","password":"secret123"}`)

	require.Equal(t, http.StatusConflict, rec.Code)
}

func TestRegister_Validation(t *testing.T) {
	tests := []struct {
		name string
		body string
		code int
	}{
		{"bad json", `not json`, 400},
		{"no @ in email", `{"email":"abc","password":"secret123"}`, 422},
		{"short password", `{"email":"a@b.com","password":"123"}`, 422},
		{"too long password", `{"email":"a@b.com","password":"` + strings.Repeat("x", 73) + `"}`, 422},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, st, _ := setup(t)
			rec := do(app, "", "POST", "/register", tt.body)
			require.Equal(t, tt.code, rec.Code)
			st.AssertNotCalled(t, "CreateUser", mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

func TestLogin_Success_CreatesSessionWithTTL(t *testing.T) {
	app, st, mr := setup(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret123"), bcrypt.MinCost)
	st.On("GetUserByEmail", mock.Anything, "a@b.com").Return(&User{ID: 5, PasswordHash: string(hash)}, nil)

	rec := do(app, "", "POST", "/login", `{"email":"A@b.com","password":"secret123"}`)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct{ Token string }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Token)

	got, err := mr.Get("session:" + resp.Token)
	require.NoError(t, err)
	require.Equal(t, "5", got)
	require.Equal(t, 24*time.Hour, mr.TTL("session:"+resp.Token))
}

func TestLogin_WrongPassword_NoSession(t *testing.T) {
	app, st, mr := setup(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret123"), bcrypt.MinCost)
	st.On("GetUserByEmail", mock.Anything, "a@b.com").Return(&User{ID: 5, PasswordHash: string(hash)}, nil)

	rec := do(app, "", "POST", "/login", `{"email":"a@b.com","password":"wrong-pass"}`)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Empty(t, mr.Keys())
}

func TestLogin_UnknownUser(t *testing.T) {
	app, st, mr := setup(t)
	st.On("GetUserByEmail", mock.Anything, mock.Anything).Return(nil, ErrNotFound)

	rec := do(app, "", "POST", "/login", `{"email":"x@y.com","password":"secret123"}`)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Empty(t, mr.Keys())
}

func TestMe_SessionExpires(t *testing.T) {
	app, st, mr := setup(t)
	mr.Set("session:tok", "5")
	mr.SetTTL("session:tok", time.Hour)
	st.On("GetUserByID", mock.Anything, int64(5)).Return(&User{ID: 5, Email: "a@b.com"}, nil)

	rec := do(app, "tok", "GET", "/me", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "a@b.com")

	mr.FastForward(2 * time.Hour) // TTL истёк

	rec = do(app, "tok", "GET", "/me", "")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestMe_NoToken(t *testing.T) {
	app, _, _ := setup(t)
	require.Equal(t, http.StatusUnauthorized, do(app, "", "GET", "/me", "").Code)
}

func TestLogout_DeletesSession(t *testing.T) {
	app, _, mr := setup(t)
	mr.Set("session:tok", "5")

	rec := do(app, "tok", "POST", "/logout", "")

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.False(t, mr.Exists("session:tok"))
	require.Equal(t, http.StatusUnauthorized, do(app, "tok", "GET", "/me", "").Code)
}