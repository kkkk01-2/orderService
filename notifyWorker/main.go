package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	defer rdb.Close()

	log.Info("worker started")
	for ctx.Err() == nil {
		// блокируемся до 5 секунд в ожидании события
		res, err := rdb.BRPop(ctx, 5*time.Second, "orders:created").Result()
		if errors.Is(err, redis.Nil) {
			continue // таймаут, событий нет
		}
		if err != nil {
			if ctx.Err() == nil {
				log.Error("brpop failed", "err", err)
				time.Sleep(time.Second)
			}
			continue
		}
		// res[0] - имя очереди, res[1] -само событие
		log.Info("notification sent", "event", res[1])
	}
	log.Info("worker stopped")
}