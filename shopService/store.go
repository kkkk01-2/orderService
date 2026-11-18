package main

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound   = errors.New("not found")
	ErrNoStock    = errors.New("insufficient stock")
	ErrEmailTaken = errors.New("email already taken")
)
// User представляет пользователя в базе
type User struct {
	ID           int64
	Email        string
	PasswordHash string
}
// Product представляет товар в базе
type Product struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	PriceCents int64  `json:"price_cents"`
	Stock      int    `json:"stock"`
}
// OrderItem представляет позицию заказа в базе
type OrderItem struct {
	ProductID int64 `json:"product_id"`
	Qty       int   `json:"qty"`
}
// OrderLine представляет позицию заказа с названием товара, для отдачи клиенту
type OrderLine struct {
	ProductName string `json:"product_name"`
	Qty         int    `json:"qty"`
	PriceCents  int64  `json:"price_cents"`
}
// Order представляет заказ с позициями, для отдачи клиенту
type Order struct {
	ID     int64       `json:"id"`
	UserID int64       `json:"user_id"`
	Status string      `json:"status"`
	Items  []OrderLine `json:"items"`
}

// Store - всё, что приложение хочет от Postgres. В тестах подменяется моком.
type Store interface {
	GetProduct(ctx context.Context, id int64) (*Product, error)
	CreateProduct(ctx context.Context, p Product) (int64, error)
	UpdateProduct(ctx context.Context, p Product) error
	CreateOrder(ctx context.Context, userID int64, items []OrderItem) (int64, error)
	GetOrder(ctx context.Context, id int64) (*Order, error)

	CreateUser(ctx context.Context, email, passwordHash string) (int64, error)
	GetUserByEmail(ctx context.Context, email string) (*User, error)
	GetUserByID(ctx context.Context, id int64) (*User, error)

	Ping(ctx context.Context) error
}
type PgStore struct{ db *pgxpool.Pool }


// GetProduct возвращает товар по id, если есть, иначе ErrNotFound
func (s *PgStore) GetProduct(ctx context.Context, id int64) (*Product, error) {
	var p Product
	err := s.db.QueryRow(ctx,
		`SELECT id, name, price_cents, stock FROM products WHERE id = $1`, id).
		Scan(&p.ID, &p.Name, &p.PriceCents, &p.Stock)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// CreateProduct создает новый товар и возвращает его ID
func (s *PgStore) CreateProduct(ctx context.Context, p Product) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx,
		`INSERT INTO products (name, price_cents, stock) VALUES ($1, $2, $3) RETURNING id`,
		p.Name, p.PriceCents, p.Stock).Scan(&id)
	return id, err
}

// UpdateProduct обновляет товар, если есть, иначе ErrNotFound
func (s *PgStore) UpdateProduct(ctx context.Context, p Product) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE products SET name = $1, price_cents = $2, stock = $3 WHERE id = $4`,
		p.Name, p.PriceCents, p.Stock, p.ID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// CreateOrder - транзакция: списать остатки + создать заказ и позиции
func (s *PgStore) CreateOrder(ctx context.Context, userID int64, items []OrderItem) (int64, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) // после Commit ничего не делает

	var orderID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO orders (user_id) VALUES ($1) RETURNING id`, userID).Scan(&orderID); err != nil {
		return 0, err
	}

	for _, it := range items {
		var price int64
		// проверка и списание одним запросом: два покупателя не купят один остаток
		err := tx.QueryRow(ctx,
			`UPDATE products SET stock = stock - $1
			 WHERE id = $2 AND stock >= $1 RETURNING price_cents`,
			it.Qty, it.ProductID).Scan(&price)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNoStock // откат всей транзакции
		}
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO order_items (order_id, product_id, qty, price_cents) VALUES ($1, $2, $3, $4)`,
			orderID, it.ProductID, it.Qty, price); err != nil {
			return 0, err
		}
	}
	return orderID, tx.Commit(ctx)
}

// GetOrder - JOIN: заказ + позиции + названия товаров
func (s *PgStore) GetOrder(ctx context.Context, id int64) (*Order, error) {
	rows, err := s.db.Query(ctx, `
		SELECT o.id, o.user_id, o.status, p.name, oi.qty, oi.price_cents
		FROM orders o
		JOIN order_items oi ON oi.order_id = o.id
		JOIN products p     ON p.id = oi.product_id
		WHERE o.id = $1
		ORDER BY oi.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var o *Order
	for rows.Next() {
		if o == nil {
			o = &Order{}
		}
		var l OrderLine
		if err := rows.Scan(&o.ID, &o.UserID, &o.Status, &l.ProductName, &l.Qty, &l.PriceCents); err != nil {
			return nil, err
		}
		o.Items = append(o.Items, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if o == nil {
		return nil, ErrNotFound
	}
	return o, nil
}

// CreateUser создает нового пользователя, хэширует пароль и сохраняет в базе
func (s *PgStore) CreateUser(ctx context.Context, email, hash string) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx,
		`INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING id`,
		email, hash).Scan(&id)

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
		return 0, ErrEmailTaken
	}
	return id, err
}

func (s *PgStore) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	return s.scanUser(ctx,
		`SELECT id, email, password_hash FROM users WHERE email = $1`, email)
}
// GetUserByID возвращает пользователя по id, если есть, иначе ErrNotFound
func (s *PgStore) GetUserByID(ctx context.Context, id int64) (*User, error) {
	return s.scanUser(ctx,
		`SELECT id, email, password_hash FROM users WHERE id = $1`, id)
}

func (s *PgStore) scanUser(ctx context.Context, query string, arg any) (*User, error) {
	var u User
	err := s.db.QueryRow(ctx, query, arg).Scan(&u.ID, &u.Email, &u.PasswordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// Ping делает healthcheck :) 
func (s *PgStore) Ping(ctx context.Context) error { return s.db.Ping(ctx) }