package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)
// TestGetProduct_CacheAsideAndTTL проверяет, что при первом запросе товар берется из базы, а при втором - из Redis, и что TTL выставляется
func TestGetProduct_CacheAsideAndTTL(t *testing.T) {
	app, st, mr := setupAuthed(t)
	st.On("GetProduct", mock.Anything, int64(1)).Return(&Product{ID: 1, Name: "Mug"}, nil)

	require.Equal(t, 200, do(app, "", "GET", "/products/1", "").Code)
	require.Equal(t, 200, do(app, "", "GET", "/products/1", "").Code)
	st.AssertNumberOfCalls(t, "GetProduct", 1) // второй раз из Redis
	require.Equal(t, 5*time.Minute, mr.TTL("product:1"))

	mr.FastForward(6 * time.Minute) // TTL истёк
	do(app, "", "GET", "/products/1", "")
	st.AssertNumberOfCalls(t, "GetProduct", 2)
}
// TestUpdateProduct_InvalidatesCache проверяет, что при обновлении товара его кэш в Redis инвалидируется
func TestUpdateProduct_InvalidatesCache(t *testing.T) {
	app, st, mr := setupAuthed(t)
	st.On("GetProduct", mock.Anything, int64(1)).Return(&Product{ID: 1, Name: "Mug"}, nil)
	st.On("UpdateProduct", mock.Anything, mock.Anything).Return(nil)

	do(app, "", "GET", "/products/1", "")
	require.True(t, mr.Exists("product:1"))

	rec := do(app, "tok", "PUT", "/products/1", `{"name":"Cup","price_cents":100,"stock":5}`)

	require.Equal(t, 200, rec.Code)
	require.False(t, mr.Exists("product:1"))
}
// TestCreateOrder_PublishesEvent проверяет, что при успешном создании заказа публикуется событие в Redis
func TestCreateOrder_PublishesEvent(t *testing.T) {
	app, st, mr := setupAuthed(t)
	items := []OrderItem{{ProductID: 1, Qty: 2}}
	st.On("CreateOrder", mock.Anything, int64(7), items).Return(int64(42), nil)

	rec := do(app, "tok", "POST", "/orders", `{"items":[{"product_id":1,"qty":2}]}`)

	require.Equal(t, http.StatusCreated, rec.Code)
	queue, err := mr.List("orders:created")
	require.NoError(t, err)
	require.Len(t, queue, 1)
	require.JSONEq(t, `{"order_id":42,"user_id":7}`, queue[0])
}
// TestCreateOrder_NoStock_NoEvent проверяет, что при попытке создать заказ с недостающим количеством товара возвращается 409 и не публикуется событие
func TestCreateOrder_NoStock_NoEvent(t *testing.T) {
	app, st, mr := setupAuthed(t)
	st.On("CreateOrder", mock.Anything, mock.Anything, mock.Anything).Return(int64(0), ErrNoStock)

	rec := do(app, "tok", "POST", "/orders", `{"items":[{"product_id":1,"qty":999}]}`)

	require.Equal(t, http.StatusConflict, rec.Code)
	require.False(t, mr.Exists("orders:created"))
}
// TestCreateOrder_Unauthorized проверяет, что без токена или с неправильным токеном возвращается 401
func TestCreateOrder_Unauthorized(t *testing.T) {
	app, _, _ := setupAuthed(t)
	require.Equal(t, 401, do(app, "", "POST", "/orders", `{}`).Code)
	require.Equal(t, 401, do(app, "bad-token", "POST", "/orders", `{}`).Code)
}
// TestCreateOrder_Validation проверяет, что при некорректных данных возвращается 422
func TestCreateOrder_Validation(t *testing.T) {
	app, _, _ := setupAuthed(t)
	require.Equal(t, 422, do(app, "tok", "POST", "/orders", `{"items":[]}`).Code)
	require.Equal(t, 422, do(app, "tok", "POST", "/orders", `{"items":[{"product_id":1,"qty":0}]}`).Code)
	require.Equal(t, 400, do(app, "tok", "POST", "/orders", `not json`).Code)
}