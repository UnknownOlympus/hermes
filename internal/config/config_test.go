package config_test

import (
	"testing"

	"github.com/Flaque/filet"
	"github.com/UnknownOlympus/hermes/internal/config"
	"github.com/stretchr/testify/assert"
)

func Test_MustLoadFromFile(t *testing.T) {
	envContent := `
HERMES_ENV=local
SCRAPER_LOGIN_URL=example.com/login
SCRAPER_USERNAME=admin
SCRAPER_PASSWORD=adminpass
SCRAPER_TARGET_URL=example.com
GRPC_PORT=:50001
`
	filet.File(t, ".env", envContent)
	defer filet.CleanUp(t)

	cfg := config.MustLoad()

	assert.Equal(t, "local", cfg.Env)
	assert.Equal(t, "example.com/login", cfg.LoginURL)
	assert.Equal(t, "admin", cfg.Username)
	assert.Equal(t, "adminpass", cfg.Password)
	assert.Equal(t, "example.com", cfg.TargetURL)
	assert.Equal(t, ":50001", cfg.GrpcPort)
}
