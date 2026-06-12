package server

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Port    int    `toml:"port"`
	Host    string `toml:"host"`
	APIKey  string `toml:"api_key"`
	DckBin  string `toml:"dck_bin"`
	DataDir string `toml:"data_dir"`
	LogDir  string `toml:"log_dir"`
}

func DefaultConfig() Config {
	return Config{
		Port:    8080,
		Host:    "0.0.0.0",
		APIKey:  "",
		DckBin:  "/usr/local/bin/dck",
		DataDir: "/var/lib/dck-wings",
		LogDir:  "/var/log/dck-wings",
	}
}

func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		val = strings.Trim(val, "\"")

		switch key {
		case "port":
			fmt.Sscanf(val, "%d", &cfg.Port)
		case "host":
			cfg.Host = val
		case "api_key":
			cfg.APIKey = val
		case "dck_bin":
			cfg.DckBin = val
		case "data_dir":
			cfg.DataDir = val
		case "log_dir":
			cfg.LogDir = val
		}
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
`
	return os.WriteFile(path, []byte(content), 0644)
}
