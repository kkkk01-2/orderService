package main

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/mock"
)

type mockStore struct {
	mock.Mock
	Store // методы, которые тест не вызывает, мокать не нужно
}

func (m *mockStore) GetProduct(ctx context.Context, id int64) (*Product, error) {
	args := m.Called(ctx, id)
	p, _ := args.Get(0).(*Product)
	return p, args.Error(1)
}

func (m *mockStore) UpdateProduct(ctx context.Context, p Product) error {
	return m.Called(ctx, p).Error(0)
}

func (m *mockStore) CreateOrder(ctx context.Context, uid int64, items []OrderItem) (int64, error) {
	args := m.Called(ctx, uid, items)
	return args.Get(0).(int64), args.Error(1)
}

func (m *mockStore) CreateUser(ctx context.Context, email, hash string) (int64, error) {
	args := m.Called(ctx, email, hash)
	return args.Get(0).(int64), args.Error(1)
}

func (m *mockStore) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	args := m.Called(ctx, email)
	u, _ := args.Get(0).(*User)
	return u, args.Error(1)
}

func (m *mockStore) GetUserByID(ctx context.Context, id int64) (*User, error) {
	args := m.Called(ctx, id)
	u, _ := args.Get(0).(*User)
	return u, args.Error(1)
}

func setup(t *testing.T) (*App, *mockStore, *miniredis.Miniredis) {
	mr := miniredis.RunT(t)
	st := new(mockStore)
	app := &App{
		store:      st,
		rdb:        redis.NewClient(&redis.Options{Addr: mr.Addr()}),
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		sessionTTL: 24 * time.Hour,
	}
	return app, st, mr
}

// setupAuthed то же, но с готовой сессией tok для пользователя 7
func setupAuthed(t *testing.T) (*App, *mockStore, *miniredis.Miniredis) {
	app, st, mr := setup(t)
	mr.Set("session:tok", "7")
	return app, st, mr
}

func do(app *App, token, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	app.routes().ServeHTTP(rec, req)
	return rec
}