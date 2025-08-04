package config

import (
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	Env       string `json:"env"`
	LoginURL  string `json:"login_url"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	TargetURL string `json:"target_url"`
	GrpcPort  string `json:"grpc_port"`
}

func MustLoad() *Config {
	_ = godotenv.Load()

	return &Config{
		Env:       os.Getenv("HERMES_ENV"),
		LoginURL:  os.Getenv("SCRAPER_LOGIN_URL"),
		Username:  os.Getenv("SCRAPER_USERNAME"),
		Password:  os.Getenv("SCRAPER_PASSWORD"),
		TargetURL: os.Getenv("SCRAPER_TARGET_URL"),
		GrpcPort:  os.Getenv("GRPC_PORT"),
	}
}
