package tgbot

import (
	"testing"
	"time"
)

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func validEnv() map[string]string {
	return map[string]string{
		"TG_API_ID":         "12345",
		"TG_API_HASH":       "deadbeef",
		"TG_SESSION_FILE":   "./main.session",
		"TG_INPUT_CHANNEL":  "@in",
		"TG_OUTPUT_CHANNEL": "@out",
		"WORKERS":           "2000",
		"PROXIES_FILE":      "./proxies/rotating.txt",
		"DB_PATH":           "./Servers.db",
		"WORK_DIR":          "./tgbot_workdir",
		"STATE_DB":          "./tgbot_state.db",
		"PROXY_REFRESH":     "10m",
	}
}

func TestLoadValid(t *testing.T) {
	setEnv(t, validEnv())
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.APIID != 12345 {
		t.Errorf("APIID = %d, want 12345", cfg.APIID)
	}
	if cfg.Workers != 2000 {
		t.Errorf("Workers = %d, want 2000", cfg.Workers)
	}
	if cfg.ProxyRefresh != 10*time.Minute {
		t.Errorf("ProxyRefresh = %v, want 10m", cfg.ProxyRefresh)
	}
	if cfg.InputChannel != "@in" || cfg.OutputChannel != "@out" {
		t.Errorf("channels = %q/%q", cfg.InputChannel, cfg.OutputChannel)
	}
}

func TestLoadProxyMutualExclusion(t *testing.T) {
	env := validEnv()
	env["PROXY_URL"] = "https://example.com/list"
	setEnv(t, env)
	if _, err := Load(""); err == nil {
		t.Fatal("expected error when PROXIES_FILE and PROXY_URL both set")
	}
}

func TestLoadMissingRequired(t *testing.T) {
	env := validEnv()
	delete(env, "TG_API_HASH")
	t.Setenv("TG_API_HASH", "")
	setEnv(t, env)
	if _, err := Load(""); err == nil {
		t.Fatal("expected error when TG_API_HASH empty")
	}
}

func TestLoadBadWorkers(t *testing.T) {
	env := validEnv()
	env["WORKERS"] = "0"
	setEnv(t, env)
	if _, err := Load(""); err == nil {
		t.Fatal("expected error when WORKERS <= 0")
	}
}
