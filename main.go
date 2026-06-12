package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"dck-wings/server"
)

var version = "1.0.0"

func main() {
	configPath := flag.String("config", "/etc/dck-wings/config.toml", "Path to config file")
	install := flag.Bool("install", false, "Install dck-wings as a systemd service")
	flag.Parse()

	if *install {
		if err := doInstall(); err != nil {
			log.Fatalf("Install failed: %v", err)
		}
		return
	}

	cfg, err := server.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("Config: %v", err)
	}

	srv := server.New(cfg)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		addr := fmt.Sprintf(":%d", cfg.Port)
		log.Printf("dck-wings %s starting on %s", version, addr)
		if err := srv.Start(addr); err != nil {
			log.Fatalf("Server: %v", err)
		}
	}()

	<-sig
	log.Println("Shutting down...")
	srv.Stop()
}

func doInstall() error {
	_, err := os.Stat("/etc/systemd/system")
	if err != nil {
		return fmt.Errorf("systemd not found")
	}

	binPath, err := os.Executable()
	if err != nil {
		return err
	}

	installDir := "/usr/local/bin"
	target := installDir + "/dck-wings"
	if err := copyFile(binPath, target, 0755); err != nil {
		return fmt.Errorf("copy binary: %w", err)
	}

	svcContent := fmt.Sprintf(`[Unit]
Description=dck-wings - Container management agent
After=network.target

[Service]
Type=simple
ExecStart=%s
Restart=always
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`, target)

	if err := os.WriteFile("/etc/systemd/system/dck-wings.service", []byte(svcContent), 0644); err != nil {
		return fmt.Errorf("write service: %w", err)
	}

	configDir := "/etc/dck-wings"
	os.MkdirAll(configDir, 0755)
	configPath := configDir + "/config.toml"
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := server.WriteDefaultConfig(configPath); err != nil {
			return fmt.Errorf("write config: %w", err)
		}
	}

	dataDir := "/var/lib/dck-wings"
	os.MkdirAll(dataDir, 0755)

	fmt.Println("dck-wings installed successfully!")
	fmt.Println("1. Edit /etc/dck-wings/config.toml and set api_key")
	fmt.Println("2. sudo systemctl daemon-reload")
	fmt.Println("3. sudo systemctl enable --now dck-wings")
	fmt.Println("4. sudo systemctl status dck-wings")

	return nil
}

func copyFile(src, dst string, perm os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, perm)
}
