package server

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Port       int    `toml:"port"`
	Host       string `toml:"host"`
	APIKey     string `toml:"api_key"`
	DckBin     string `toml:"dck_bin"`
	DataDir    string `toml:"data_dir"`
	LogDir     string `toml:"log_dir"`
	TLSCert    string `toml:"tls_cert"`
	TLSKey     string `toml:"tls_key"`
	DckTimeout int    `toml:"dck_timeout"`
	AuditSize  int    `toml:"audit_size"`
	Version    string `toml:"-"` // injected at build time
}

func DefaultConfig() Config {
	return Config{
		Port:       8080,
		Host:       "0.0.0.0",
		APIKey:     "",
		DckBin:     "/usr/local/bin/dck",
		DataDir:    "/var/lib/dck-wings",
		LogDir:     "/var/log/dck-wings",
		DckTimeout: 60,
		AuditSize:  1000,
	}
}

func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()

	_, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return cfg, fmt.Errorf("decode config: %w", err)
	}

	if cfg.APIKey == "" {
		return cfg, fmt.Errorf("api_key is required in config")
	}

	return cfg, nil
}

func WriteDefaultConfig(path string) error {
	content := `# dck-wings configuration
port = 8080
host = "0.0.0.0"
api_key = "change-me"
dck_bin = "/usr/local/bin/dck"
data_dir = "/var/lib/dck-wings"
log_dir = "/var/log/dck-wings"
tls_cert = ""
tls_key = ""
dck_timeout = 60
audit_size = 1000
`
	return os.WriteFile(path, []byte(content), 0644)
}
