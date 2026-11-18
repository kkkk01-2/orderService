SHOP_DSN=postgres://app:app@localhost:5432/shop?sslmode=disable

infra:
	docker compose up -d postgres redis

migrate:
	goose -dir shopService/migrations postgres "$(SHOP_DSN)" up

test:
	cd shopService && go test ./...

up:
	docker compose up --build