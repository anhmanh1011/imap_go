package tgbot

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

// Config holds every tunable for the bot: Telegram credentials, the two
// channels, and the checker parameters forwarded to processor.Process.
type Config struct {
	APIID         int
	APIHash       string
	SessionPath   string
	InputChannel  string // @username form (v1)
	OutputChannel string // @username form (v1)

	Workers      int
	ProxiesFile  string
	ProxyURL     string
	ProxyRefresh time.Duration
	DBPath       string
	WorkDir      string
	StateDB      string
	SearchFrom   string // if set, run inbox-search for this address instead of plain checker
}

// Load reads configuration from environment variables. If envPath is
// non-empty and the file exists, it is loaded into the environment first
// (existing env vars take precedence — godotenv does not overwrite).
func Load(envPath string) (Config, error) {
	if envPath != "" {
		if _, err := os.Stat(envPath); err == nil {
			if err := godotenv.Load(envPath); err != nil {
				return Config{}, fmt.Errorf("load %s: %w", envPath, err)
			}
		}
	}

	var c Config
	var err error

	if c.APIID, err = strconv.Atoi(os.Getenv("TG_API_ID")); err != nil {
		return Config{}, fmt.Errorf("TG_API_ID must be an integer: %w", err)
	}
	c.APIHash = os.Getenv("TG_API_HASH")
	c.SessionPath = os.Getenv("TG_SESSION_FILE")
	c.InputChannel = os.Getenv("TG_INPUT_CHANNEL")
	c.OutputChannel = os.Getenv("TG_OUTPUT_CHANNEL")
	c.ProxiesFile = os.Getenv("PROXIES_FILE")
	c.ProxyURL = os.Getenv("PROXY_URL")
	c.SearchFrom = os.Getenv("SEARCH_FROM")
	c.DBPath = getenvDefault("DB_PATH", "./Servers.db")
	c.WorkDir = getenvDefault("WORK_DIR", "./tgbot_workdir")
	c.StateDB = getenvDefault("STATE_DB", "./tgbot_state.db")

	if c.Workers, err = strconv.Atoi(os.Getenv("WORKERS")); err != nil {
		return Config{}, fmt.Errorf("WORKERS must be an integer: %w", err)
	}

	refresh := getenvDefault("PROXY_REFRESH", "10m")
	if c.ProxyRefresh, err = time.ParseDuration(refresh); err != nil {
		return Config{}, fmt.Errorf("PROXY_REFRESH bad duration: %w", err)
	}

	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) validate() error {
	if c.APIHash == "" {
		return fmt.Errorf("TG_API_HASH is required")
	}
	if c.SessionPath == "" {
		return fmt.Errorf("TG_SESSION_FILE is required")
	}
	if c.InputChannel == "" || c.OutputChannel == "" {
		return fmt.Errorf("TG_INPUT_CHANNEL and TG_OUTPUT_CHANNEL are required")
	}
	if c.Workers <= 0 {
		return fmt.Errorf("WORKERS must be a positive integer")
	}
	if c.ProxiesFile != "" && c.ProxyURL != "" {
		return fmt.Errorf("PROXIES_FILE and PROXY_URL are mutually exclusive")
	}
	return nil
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
