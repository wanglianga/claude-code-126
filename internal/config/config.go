package config

import (
	"os"
)

type Config struct {
	HTTPAddr       string
	DatabaseURL    string
	RedisAddr      string
	PlatformAPIKey string
	TokenTTLHours  int
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func Load() Config {
	return Config{
		HTTPAddr:       getenv("HTTP_ADDR", ":8080"),
		DatabaseURL:    getenv("DATABASE_URL", "postgres://coffee:coffee@127.0.0.1:5432/coffeechain?sslmode=disable"),
		RedisAddr:      getenv("REDIS_ADDR", "127.0.0.1:6379"),
		PlatformAPIKey: getenv("PLATFORM_API_KEY", "platform-demo-key"),
		TokenTTLHours:  12,
	}
}
