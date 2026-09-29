package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"coffeechain/internal/api"
	"coffeechain/internal/auth"
	"coffeechain/internal/cache"
	"coffeechain/internal/config"
	"coffeechain/internal/db"
	"coffeechain/internal/service"
)

func main() {
	cfg := config.Load()

	database, err := db.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("连接 PostgreSQL 失败: %v", err)
	}
	defer database.Close()
	if err := db.Migrate(database); err != nil {
		log.Fatalf("数据库迁移失败: %v", err)
	}
	log.Println("数据库迁移完成")

	rdb := cache.New(cfg.RedisAddr)
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		log.Fatalf("连接 Redis 失败: %v", err)
	}

	am := auth.NewManager(rdb, database, cfg.PlatformAPIKey)
	if err := am.EnsurePasswords(); err != nil {
		log.Fatalf("初始化账号密码失败: %v", err)
	}

	app := service.New(database, rdb)

	// 定时扫描待取区放置过久饮品（每 60 秒）
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := app.SweepStale(context.Background()); err != nil {
					log.Printf("stale sweep 错误: %v", err)
				}
			case <-stop:
				return
			}
		}
	}()

	handler := api.NewServer(app, am, cfg.PlatformAPIKey)
	log.Printf("咖啡连锁外卖服务启动: http://localhost%s", cfg.HTTPAddr)
	log.Printf("Swagger UI: http://localhost%s/swagger/", cfg.HTTPAddr)
	if err := http.ListenAndServe(cfg.HTTPAddr, handler); err != nil {
		log.Fatalf("HTTP 服务退出: %v", err)
	}
}
