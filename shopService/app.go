package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/redis/go-redis/v9"
)

const (
	productTTL  = 5 * time.Minute
	ordersQueue = "orders:created"
)

type App struct {
	store      Store
	rdb        *redis.Client
	log        *slog.Logger
	sessionTTL time.Duration
} 

type ctxKey struct{}

func productKey(id int64) string { return fmt.Sprintf("product:%d", id) }

func (a *App) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)

	// открытые маршруты
	r.Get("/healthz", a.healthz)
	r.Post("/register", a.register)
	r.Post("/login", a.login)
	r.Post("/logout", a.logout)
	r.Get("/products/{id}", a.getProduct)

	// маршруты, для которых нужна сессия
	r.Group(func(r chi.Router) {
		r.Use(a.auth)
		r.Get("/me", a.me)
		r.Post("/products", a.createProduct)
		r.Put("/products/{id}", a.updateProduct)
		r.Post("/orders", a.createOrder)
		r.Get("/orders/{id}", a.getOrder)
	})
	return r
	
} 

// healthz проверяет доступность PostgreSQL и Redis
func (a *App) healthz(w http.ResponseWriter, r *http.Request) {
	if err := a.store.Ping(r.Context()); err != nil {
		http.Error(w, "postgres down", http.StatusServiceUnavailable)
		return
	}
	if err := a.rdb.Ping(r.Context()).Err(); err != nil {
		http.Error(w, "redis down", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// auth проверяет сессию в Redis (ключ session:{token}) и кладёт userID в контекст.
func (a *App) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, err := a.rdb.Get(r.Context(), sessionKey(bearerToken(r))).Int64()
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, uid)))
	})
}



func (a *App) getProduct(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	if raw, err := a.rdb.Get(ctx, productKey(id)).Bytes(); err == nil {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
		return
	}

	p, err := a.store.GetProduct(ctx, id)
	if err != nil {
		a.fail(w, err)
		return
	}
	raw, _ := json.Marshal(p)
	if err := a.rdb.Set(ctx, productKey(id), raw, productTTL).Err(); err != nil {
		a.log.Warn("cache set failed", "err", err)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func (a *App) createProduct(w http.ResponseWriter, r *http.Request) {
	var p Product
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if strings.TrimSpace(p.Name) == "" || p.PriceCents < 0 || p.Stock < 0 {
		writeErr(w, http.StatusUnprocessableEntity, "validation failed")
		return
	}
	id, err := a.store.CreateProduct(r.Context(), p)
	if err != nil {
		a.fail(w, err)
		return
	}
	p.ID = id
	writeJSON(w, http.StatusCreated, p)
}

func (a *App) updateProduct(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var p Product
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if strings.TrimSpace(p.Name) == "" || p.PriceCents < 0 || p.Stock < 0 {
		writeErr(w, http.StatusUnprocessableEntity, "validation failed")
		return
	}
	p.ID = id
	if err := a.store.UpdateProduct(r.Context(), p); err != nil {
		a.fail(w, err)
		return
	}
	a.rdb.Del(r.Context(), productKey(id)) // инвалидация кеша
	writeJSON(w, http.StatusOK, p)
}


func (a *App) createOrder(w http.ResponseWriter, r *http.Request) {
	uid := r.Context().Value(ctxKey{}).(int64)

	var req struct {
		Items []OrderItem `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if len(req.Items) == 0 {
		writeErr(w, http.StatusUnprocessableEntity, "items required")
		return
	}
	for _, it := range req.Items {
		if it.Qty <= 0 {
			writeErr(w, http.StatusUnprocessableEntity, "qty must be positive")
			return
		}
	}

	id, err := a.store.CreateOrder(r.Context(), uid, req.Items)
	if err != nil {
		a.fail(w, err)
		return
	}

	// остатки изменились, значит кеш этих товаров устарел
	for _, it := range req.Items {
		a.rdb.Del(r.Context(), productKey(it.ProductID))
	}

	// событие для workera; заказ уже сохранён, поэтому ошибку только логируем
	event, _ := json.Marshal(map[string]int64{"order_id": id, "user_id": uid})
	if err := a.rdb.LPush(r.Context(), ordersQueue, event).Err(); err != nil {
		a.log.Error("publish failed", "order_id", id, "err", err)
	}

	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (a *App) getOrder(w http.ResponseWriter, r *http.Request) {
	uid := r.Context().Value(ctxKey{}).(int64)
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	o, err := a.store.GetOrder(r.Context(), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	if o.UserID != uid { // чужой заказ выглядит как несуществующий
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, o)
}


func (a *App) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrNoStock):
		writeErr(w, http.StatusConflict, "insufficient stock")
	default:
		a.log.Error("internal error", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
}

func parseID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}