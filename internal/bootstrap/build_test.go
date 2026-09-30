package bootstrap

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nhirsama/yukibot/internal/config"
)

func TestBuildDoesNotConnect(t *testing.T) {
	application, err := Build(config.Settings{
		TelegramAPIID:          1,
		TelegramAPIHash:        "hash",
		TelegramSessionPath:    filepath.Join(t.TempDir(), "yukibot.session"),
		DatabaseURL:            "postgres://localhost:5432/yukibot?sslmode=disable",
		LogLevel:               "INFO",
		ForwarderAlbumDelay:    800 * time.Millisecond,
		ShutdownTimeout:        15 * time.Second,
		RebuildJoinMinInterval: 300 * time.Second,
		RebuildJoinMaxInterval: 600 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if application == nil || application.Lifecycle == nil {
		t.Fatal("expected an application")
	}
}
