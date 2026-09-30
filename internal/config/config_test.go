package config

import (
	"strings"
	"testing"
)

func TestLoadFromEnvironment(t *testing.T) {
	t.Setenv("YUKIBOT_TELEGRAM_API_ID", "12345")
	t.Setenv("YUKIBOT_TELEGRAM_API_HASH", "secret-hash")
	t.Setenv("YUKIBOT_LOG_LEVEL", "debug")
	settings, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if settings.TelegramAPIID != 12345 || settings.TelegramAPIHash != "secret-hash" || settings.LogLevel != "DEBUG" {
		t.Fatalf("%+v", settings)
	}
	if settings.TelegramSessionPath != "data/yukibot.session" {
		t.Fatalf("session %s", settings.TelegramSessionPath)
	}
	if !strings.HasPrefix(settings.DatabaseURL, "postgres://") {
		t.Fatalf("url %s", settings.DatabaseURL)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	t.Setenv("YUKIBOT_TELEGRAM_API_ID", "0")
	t.Setenv("YUKIBOT_TELEGRAM_API_HASH", "secret-hash")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "greater than 0") {
		t.Fatalf("id: %v", err)
	}
	t.Setenv("YUKIBOT_TELEGRAM_API_ID", "1")
	t.Setenv("YUKIBOT_DATABASE_URL", "sqlite:///data/yukibot.db")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "only postgres") {
		t.Fatalf("db: %v", err)
	}
	t.Setenv("YUKIBOT_DATABASE_URL", "postgres://localhost/yukibot")
	t.Setenv("YUKIBOT_LOG_LEVEL", "verbose")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "unknown log level") {
		t.Fatalf("level: %v", err)
	}
	t.Setenv("YUKIBOT_LOG_LEVEL", "INFO")
	t.Setenv("YUKIBOT_FORWARDER_ALBUM_DELAY", "-1")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "greater than or equal to 0") {
		t.Fatalf("delay: %v", err)
	}
	t.Setenv("YUKIBOT_FORWARDER_ALBUM_DELAY", "0.8")
	t.Setenv("YUKIBOT_REBUILD_JOIN_MIN_INTERVAL", "299")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "greater than or equal to 300") {
		t.Fatalf("min: %v", err)
	}
	t.Setenv("YUKIBOT_REBUILD_JOIN_MIN_INTERVAL", "601")
	t.Setenv("YUKIBOT_REBUILD_JOIN_MAX_INTERVAL", "600")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "must be ordered") {
		t.Fatalf("order: %v", err)
	}
}
