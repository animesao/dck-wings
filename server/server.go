package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type Server struct {
	cfg    Config
	server *http.Server
	mu     sync.Mutex
}

func New(cfg Config) *Server {
	s := &Server{cfg: cfg}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/containers", s.handleContainers)
	mux.HandleFunc("/api/containers/", s.handleContainerByID)

	s.server = &http.Server{
		Handler: s.authMiddleware(mux),
	}
	return s
}

func (s *Server) Start(addr string) error {
	s.server.Addr = addr
	return s.server.ListenAndServe()
}

func (s *Server) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.server.Shutdown(ctx)
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("Authorization")
		if token == "" {
			token = r.URL.Query().Get("api_key")
		}
		if strings.HasPrefix(token, "Bearer ") {
			token = token[7:]
		}
		if token != s.cfg.APIKey {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) dck(args ...string) (string, error) {
	cmd := exec.Command(s.cfg.DckBin, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (s *Server) dckWithInput(input string, args ...string) (string, error) {
	cmd := exec.Command(s.cfg.DckBin, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	go func() {
		defer stdin.Close()
		io.WriteString(stdin, input)
	}()
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

type ContainerInfo struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	Status  string            `json:"status"`
	Ports   []string          `json:"ports,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
	Cmd     string            `json:"cmd,omitempty"`
	Created string            `json:"created,omitempty"`
}

func (s *Server) handleContainers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		s.listContainers(w, r)
	case "POST":
		s.createContainer(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) listContainers(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") == "1"
	args := []string{"ps"}
	if all {
		args = append(args, "-a")
	}

	out, err := s.dck(args...)
	if err != nil {
		json.NewEncoder(w).Encode(containerListResult{Containers: []ContainerInfo{}, Error: err.Error()})
		return
	}

	containers := parsePSOutput(out)
	json.NewEncoder(w).Encode(containerListResult{Containers: containers})
}

type containerListResult struct {
	Containers []ContainerInfo `json:"containers"`
	Error      string          `json:"error,omitempty"`
}

func parsePSOutput(out string) []ContainerInfo {
	var containers []ContainerInfo
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		containers = append(containers, ContainerInfo{
			ID:     fields[0],
			Image:  fields[1],
			Status: fields[2],
			Name:   fields[3],
		})
	}
	return containers
}

func (s *Server) createContainer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Image       string   `json:"image"`
		Name        string   `json:"name"`
		Cmd         []string `json:"cmd"`
		Ports       []string `json:"ports"`
		Volumes     []string `json:"volumes"`
		Env         []string `json:"env"`
		Detach      bool     `json:"detach"`
		Interactive bool     `json:"interactive"`
		Memory      string   `json:"memory"`
		CPUs        float64  `json:"cpus"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Image == "" {
		http.Error(w, "image is required", http.StatusBadRequest)
		return
	}

	args := []string{"run"}
	if req.Detach {
		args = append(args, "-d")
	}
	if req.Interactive {
		args = append(args, "-i", "-t")
	}
	if req.Name != "" {
		args = append(args, "-n", req.Name)
	}
	if req.Memory != "" {
		args = append(args, "--memory", req.Memory)
	}
	if req.CPUs > 0 {
		args = append(args, "--cpus", fmt.Sprintf("%.1f", req.CPUs))
	}
	for _, p := range req.Ports {
		args = append(args, "-p", p)
	}
	for _, v := range req.Volumes {
		args = append(args, "-v", v)
	}
	for _, e := range req.Env {
		args = append(args, "-e", e)
	}
	args = append(args, req.Image)
	if len(req.Cmd) > 0 {
		args = append(args, req.Cmd...)
	}

	out, err := s.dck(args...)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("%s: %s", err.Error(), out)})
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"id": out})
}

func (s *Server) handleContainerByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/containers/")
	id = strings.TrimSuffix(id, "/")
	if id == "" {
		http.Error(w, "Container ID required", http.StatusBadRequest)
		return
	}

	if strings.Contains(id, "/") {
		parts := strings.SplitN(id, "/", 2)
		containerID := parts[0]
		action := parts[1]

		switch action {
		case "start":
			s.containerAction(w, containerID, "start")
		case "stop":
			s.containerAction(w, containerID, "stop")
		case "restart":
			s.containerAction(w, containerID, "restart")
		case "remove":
			s.containerRemove(w, containerID, r)
		case "logs":
			s.containerLogs(w, containerID, r)
		case "stats":
			s.containerStats(w, containerID)
		case "state":
			s.containerState(w, containerID)
		case "exec":
			s.containerExec(w, containerID, r)
		default:
			http.Error(w, "Unknown action", http.StatusNotFound)
		}
		return
	}

	switch r.Method {
	case "GET":
		s.containerState(w, id)
	case "DELETE":
		s.containerRemove(w, id, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) containerAction(w http.ResponseWriter, id, action string) {
	out, err := s.dck(action, id)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("%s: %s", err.Error(), out)})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "id": out})
}

func (s *Server) containerRemove(w http.ResponseWriter, id string, r *http.Request) {
	args := []string{"rm"}
	if r.URL.Query().Get("force") == "1" {
		args = append(args, "-f")
	}
	args = append(args, id)

	out, err := s.dck(args...)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("%s: %s", err.Error(), out)})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "removed", "id": out})
}

func (s *Server) containerLogs(w http.ResponseWriter, id string, r *http.Request) {
	follow := r.URL.Query().Get("follow") == "1"
	tail := r.URL.Query().Get("tail")

	args := []string{"logs", id}
	if follow {
		args = append(args, "-f")
	}
	if tail != "" {
		args = append(args, "--tail", tail)
	}

	cmd := exec.Command(s.cfg.DckBin, args...)
	if follow {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		cmd.Stdout = w
		cmd.Stderr = w
		cmd.Run()
		return
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"logs": string(out)})
}

func (s *Server) containerStats(w http.ResponseWriter, id string) {
	out, err := s.dck("stats", id)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"stats": out})
}

func (s *Server) containerState(w http.ResponseWriter, id string) {
	out, err := s.dck("ps", "-a")
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"error": err.Error()})
		return
	}

	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		if fields[0] == id || fields[3] == id {
			json.NewEncoder(w).Encode(map[string]string{
				"id":     fields[0],
				"image":  fields[1],
				"status": fields[2],
				"name":   fields[3],
			})
			return
		}
	}
	json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
}

func (s *Server) containerExec(w http.ResponseWriter, id string, r *http.Request) {
	var req struct {
		Cmd []string `json:"cmd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if len(req.Cmd) == 0 {
		http.Error(w, "cmd is required", http.StatusBadRequest)
		return
	}

	args := append([]string{"exec", id}, req.Cmd...)
	out, err := s.dck(args...)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error(), "output": out})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"output": out})
}
