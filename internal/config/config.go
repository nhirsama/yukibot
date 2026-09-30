package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Settings is the validated process configuration.
type Settings struct {
	TelegramAPIID          int
	TelegramAPIHash        string
	TelegramSessionPath    string
	DatabaseURL            string
	LogLevel               string
	ForwarderAlbumDelay    time.Duration
	ShutdownTimeout        time.Duration
	RebuildJoinMinInterval time.Duration
	RebuildJoinMaxInterval time.Duration
}

// Load reads .env (when present) and YUKIBOT_* environment variables.
// Existing process environment wins over the file.
func Load() (Settings, error) {
	fileValues, err := readEnvFile(".env")
	if err != nil {
		return Settings{}, err
	}
	lookup := func(key string) (string, bool) {
		if value, ok := os.LookupEnv("YUKIBOT_" + key); ok {
			return value, true
		}
		value, ok := fileValues[key]
		return value, ok
	}
	settings := Settings{
		TelegramSessionPath:    "data/yukibot.session",
		DatabaseURL:            "postgres://localhost:5432/yukibot?sslmode=disable",
		LogLevel:               "INFO",
		ForwarderAlbumDelay:    800 * time.Millisecond,
		ShutdownTimeout:        15 * time.Second,
		RebuildJoinMinInterval: 300 * time.Second,
		RebuildJoinMaxInterval: 600 * time.Second,
	}
	if value, ok := lookup("TELEGRAM_API_ID"); ok {
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || parsed <= 0 {
			return Settings{}, fmt.Errorf("telegram_api_id must be greater than 0")
		}
		settings.TelegramAPIID = parsed
	} else {
		return Settings{}, fmt.Errorf("telegram_api_id must be greater than 0")
	}
	hash, ok := lookup("TELEGRAM_API_HASH")
	if !ok || strings.TrimSpace(hash) == "" {
		return Settings{}, fmt.Errorf("telegram_api_hash must not be blank")
	}
	settings.TelegramAPIHash = hash
	if value, ok := lookup("TELEGRAM_SESSION_PATH"); ok && strings.TrimSpace(value) != "" {
		settings.TelegramSessionPath = value
	}
	if value, ok := lookup("DATABASE_URL"); ok && strings.TrimSpace(value) != "" {
		settings.DatabaseURL = strings.TrimSpace(value)
	}
	if !strings.HasPrefix(settings.DatabaseURL, "postgres://") && !strings.HasPrefix(settings.DatabaseURL, "postgresql://") {
		return Settings{}, fmt.Errorf("only postgres:// database URLs are currently supported")
	}
	if value, ok := lookup("LOG_LEVEL"); ok {
		settings.LogLevel = strings.ToUpper(strings.TrimSpace(value))
	}
	if !knownLevel(settings.LogLevel) {
		return Settings{}, fmt.Errorf("unknown log level: %s", settings.LogLevel)
	}
	if value, ok := lookup("FORWARDER_ALBUM_DELAY"); ok {
		seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || seconds < 0 || seconds > 10 {
			return Settings{}, fmt.Errorf("forwarder_album_delay must be greater than or equal to 0 and at most 10")
		}
		settings.ForwarderAlbumDelay = time.Duration(seconds * float64(time.Second))
	}
	if value, ok := lookup("SHUTDOWN_TIMEOUT"); ok {
		seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || seconds <= 0 || seconds > 300 {
			return Settings{}, fmt.Errorf("shutdown_timeout must be greater than 0 and at most 300")
		}
		settings.ShutdownTimeout = time.Duration(seconds * float64(time.Second))
	}
	if value, ok := lookup("REBUILD_JOIN_MIN_INTERVAL"); ok {
		seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || seconds < 300 || seconds > 86400 {
			return Settings{}, fmt.Errorf("rebuild_join_min_interval must be greater than or equal to 300")
		}
		settings.RebuildJoinMinInterval = time.Duration(seconds * float64(time.Second))
	}
	if value, ok := lookup("REBUILD_JOIN_MAX_INTERVAL"); ok {
		seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || seconds < 300 || seconds > 86400 {
			return Settings{}, fmt.Errorf("rebuild_join_max_interval must be greater than or equal to 300")
		}
		settings.RebuildJoinMaxInterval = time.Duration(seconds * float64(time.Second))
	}
	if settings.RebuildJoinMaxInterval < settings.RebuildJoinMinInterval {
		return Settings{}, fmt.Errorf("rebuild join intervals must be ordered")
	}
	return settings, nil
}

func knownLevel(level string) bool {
	switch level {
	case "DEBUG", "INFO", "WARN", "WARNING", "ERROR", "CRITICAL":
		return true
	default:
		return false
	}
}

func readEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	defer file.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		key = strings.TrimPrefix(key, "YUKIBOT_")
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key != "" && validKey(key) {
			values[key] = value
		}
	}
	return values, scanner.Err()
}

func validKey(key string) bool {
	for _, r := range key {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			continue
		}
		return false
	}
	return true
}

// SessionDir is the parent directory of the session file.
func (s Settings) SessionDir() string {
	return filepath.Dir(s.TelegramSessionPath)
}
