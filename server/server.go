package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		host := r.Host
		allowed := strings.HasPrefix(origin, "http://"+host) ||
			strings.HasPrefix(origin, "https://"+host) ||
			strings.HasPrefix(origin, "http://localhost") ||
			strings.HasPrefix(origin, "https://localhost") ||
			strings.HasPrefix(origin, "http://127.0.0.1") ||
			strings.HasPrefix(origin, "https://127.0.0.1")
		if !allowed {
			log.Printf("[WARN] WebSocket connection rejected from origin: %s", origin)
		}
		return allowed
	},
}

type rateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	limit    int
	window   time.Duration
}

type visitor struct {
	count    int
	lastSeen time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	rl := &rateLimiter{
		visitors: make(map[string]*visitor),
		limit:    limit,
		window:   window,
	}
	go func() {
		for range time.NewTicker(10 * time.Minute).C {
			rl.mu.Lock()
			for ip, v := range rl.visitors {
				if time.Since(v.lastSeen) > 2*window {
					delete(rl.visitors, ip)
				}
			}
			rl.mu.Unlock()
		}
	}()
	return rl
}

func (rl *rateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	v, exists := rl.visitors[ip]
	if !exists {
		rl.visitors[ip] = &visitor{count: 1, lastSeen: time.Now()}
		return true
	}
	if time.Since(v.lastSeen) > rl.window {
		v.count = 1
		v.lastSeen = time.Now()
		return true
	}
	v.count++
	v.lastSeen = time.Now()
	return v.count <= rl.limit
}

type Server struct {
	cfg         Config
	server      *http.Server
	rateLimiter *rateLimiter
}

func New(cfg Config) *Server {
	s := &Server{cfg: cfg, rateLimiter: newRateLimiter(100, time.Second)}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/health", s.handleHealth)

	mux.HandleFunc("/api/containers", s.handleContainers)
	mux.HandleFunc("/api/containers/", s.handleContainerByID)

	mux.HandleFunc("/api/images", s.handleImages)
	mux.HandleFunc("/api/images/", s.handleImageByRef)

	mux.HandleFunc("/api/system/prune", s.handleSystemPrune)
	mux.HandleFunc("/api/system/stop-all", s.handleStopAll)

	s.server = &http.Server{
		Handler:      s.authMiddleware(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	return s
}

func (s *Server) Start(addr string) error {
	s.server.Addr = addr
	if s.cfg.LogDir != "" {
		os.MkdirAll(s.cfg.LogDir, 0755)
		logPath := filepath.Join(s.cfg.LogDir, "server.log")
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err == nil {
			log.SetOutput(io.MultiWriter(os.Stderr, f))
		}
	}
	if s.cfg.TLSCert != "" && s.cfg.TLSKey != "" {
		return s.server.ListenAndServeTLS(s.cfg.TLSCert, s.cfg.TLSKey)
	}
	return s.server.ListenAndServe()
}

func (s *Server) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.server.Shutdown(ctx)
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientIP := r.RemoteAddr
		if idx := strings.LastIndex(clientIP, ":"); idx != -1 {
			clientIP = clientIP[:idx]
		}
		if !s.rateLimiter.Allow(clientIP) {
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		token := r.Header.Get("Authorization")
		if strings.HasPrefix(token, "Bearer ") {
			token = token[7:]
		} else {
			// Do NOT accept api_key from URL query parameter (security: prevents log leakage)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if token != s.cfg.APIKey {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) dckTimeout() time.Duration {
	if s.cfg.DckTimeout > 0 {
		return time.Duration(s.cfg.DckTimeout) * time.Second
	}
	return 60 * time.Second
}

// --- dck CLI wrapper ---

func (s *Server) dck(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.dckTimeout())
	defer cancel()
	cmd := exec.CommandContext(ctx, s.cfg.DckBin, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (s *Server) dckWithStdin(input io.Reader, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.dckTimeout())
	defer cancel()
	cmd := exec.CommandContext(ctx, s.cfg.DckBin, args...)
	cmd.Stdin = input
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (s *Server) dckStream(w io.Writer, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.dckTimeout())
	defer cancel()
	cmd := exec.CommandContext(ctx, s.cfg.DckBin, args...)
	cmd.Stdout = w
	cmd.Stderr = w
	return cmd.Run()
}

// --- readContainerState reads the container state JSON from disk ---

func (s *Server) readContainerState(id string) (map[string]interface{}, error) {
	home := s.cfg.DataDir
	statePath := filepath.Join(home, "containers", id+".json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		return nil, err
	}
	var state map[string]interface{}
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return state, nil
}

// --- Health ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()
	cpuModel := readCPUModel()
	cpuCores := runtime.NumCPU()
	memTotal, memUsed, memPct := readMemInfo()
	diskTotal, diskUsed, diskPct := readDiskUsage("/")
	uptime := readUptime()

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":      "ok",
		"version":     "1.3.0",
		"dck":         s.cfg.DckBin,
		"hostname":    hostname,
		"cpu_model":   cpuModel,
		"cpu_cores":   cpuCores,
		"cpu_percent": getCPUPercent(),
		"mem_total":   memTotal,
		"mem_used":    memUsed,
		"mem_percent": memPct,
		"disk_total":  diskTotal,
		"disk_used":   diskUsed,
		"disk_percent": diskPct,
		"uptime":      uptime,
	})
}

func readCPUModel() string {
	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "model name") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

func readMemInfo() (total, used uint64, pct float64) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, 0
	}
	var memTotal, memAvail uint64
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fmt.Sscanf(line, "MemTotal: %d kB", &memTotal)
		}
		if strings.HasPrefix(line, "MemAvailable:") {
			fmt.Sscanf(line, "MemAvailable: %d kB", &memAvail)
		}
	}
	memTotal *= 1024
	memAvail *= 1024
	if memTotal > 0 {
		used = memTotal - memAvail
		pct = float64(used) / float64(memTotal) * 100
	}
	return memTotal, used, pct
}

func readUptime() string {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return ""
	}
	var secs float64
	fmt.Sscanf(string(b), "%f", &secs)
	d := time.Duration(secs) * time.Second
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	}
	return fmt.Sprintf("%dh %dm", hours, mins)
}

func getCPUPercent() float64 {
	type cpuTimes struct{ user, nice, system, idle uint64 }
	readCPU := func() cpuTimes {
		b, err := os.ReadFile("/proc/stat")
		if err != nil {
			return cpuTimes{}
		}
		var ct cpuTimes
		fmt.Sscanf(string(b), "cpu %d %d %d %d", &ct.user, &ct.nice, &ct.system, &ct.idle)
		return ct
	}
	t1 := readCPU()
	if t1.user == 0 {
		return 0
	}
	time.Sleep(100 * time.Millisecond)
	t2 := readCPU()
	total1 := t1.user + t1.nice + t1.system + t1.idle
	total2 := t2.user + t2.nice + t2.system + t2.idle
	totalDelta := total2 - total1
	idleDelta := t2.idle - t1.idle
	if totalDelta == 0 {
		return 0
	}
	return float64(totalDelta-idleDelta) / float64(totalDelta) * 100
}

// --- Container CRUD ---

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

type containerListResult struct {
	Containers []ContainerInfo `json:"containers"`
	Error      string          `json:"error,omitempty"`
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
	home := s.cfg.DataDir
	cdir := filepath.Join(home, "containers")

	entries, err := os.ReadDir(cdir)
	if err != nil {
		json.NewEncoder(w).Encode(containerListResult{Containers: []ContainerInfo{}})
		return
	}

	var containers []ContainerInfo
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		state, err := s.readContainerState(id)
		if err != nil {
			continue
		}

		ci := ContainerInfo{ID: id}
		if name, ok := state["name"].(string); ok {
			ci.Name = name
		}
		if image, ok := state["image_name"].(string); ok {
			ci.Image = image
			if tag, ok := state["image_tag"].(string); ok && tag != "" {
				ci.Image += ":" + tag
			}
		}
		if status, ok := state["status"].(string); ok {
			ci.Status = status
		}

		if !all && ci.Status != "running" {
			continue
		}
		containers = append(containers, ci)
	}

	json.NewEncoder(w).Encode(containerListResult{Containers: containers})
}

func (s *Server) createContainer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Image         string   `json:"image"`
		Name          string   `json:"name"`
		Cmd           []string `json:"cmd"`
		Ports         []string `json:"ports"`
		Volumes       []string `json:"volumes"`
		Env           []string `json:"env"`
		Detach        bool     `json:"detach"`
		Interactive   bool     `json:"interactive"`
		Memory        string   `json:"memory"`
		CPUs          float64  `json:"cpus"`
		Network       string   `json:"network"`
		Restart       string   `json:"restart"`
		StartupScript string   `json:"startup_script"`
		Disk          string   `json:"disk"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
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
	if req.Network != "" {
		args = append(args, "--network", req.Network)
	}
	if req.Restart != "" {
		args = append(args, "--restart", req.Restart)
	}
	if req.StartupScript != "" {
		args = append(args, "--startup", req.StartupScript)
	}
	if req.Disk != "" {
		args = append(args, "--disk", req.Disk)
	}
	for _, p := range req.Ports {
		args = append(args, "-p", p)
	}
	// Auto-mount /home/container + /data to a unique named volume
	// so each container gets its own isolated filesystem.
	// dck's -v flag is a single comma-separated string, so combine all.
	volBytes := make([]byte, 8)
	rand.Read(volBytes)
	volName := hex.EncodeToString(volBytes)
	var volArgs []string
	hasHomeVolume := false
	for _, v := range req.Volumes {
		parts := strings.SplitN(v, ":", 2)
		if len(parts) == 2 && parts[1] == "/home/container" {
			hasHomeVolume = true
		}
		volArgs = append(volArgs, v)
	}
	if !hasHomeVolume {
		volArgs = append(volArgs, "data_"+volName+":/home/container")
		volArgs = append(volArgs, "data_"+volName+":/data")
	}
	if len(volArgs) > 0 {
		args = append(args, "-v", strings.Join(volArgs, ","))
	}
	// Default workdir to /home/container
	args = append(args, "--workdir", "/home/container")
	// Common data directory env vars (itzg/minecraft-server, etc.)
	args = append(args, "-e", "DATA_DIR=/home/container")
	args = append(args, "-e", "DATA_PATH=/home/container")
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
	if id == "" || strings.Contains(id, "..") || strings.Contains(id, "/") || strings.Contains(id, "\\") || strings.Contains(id, "\x00") {
		http.Error(w, "Invalid container ID", http.StatusBadRequest)
		return
	}

	if strings.Contains(id, "/") {
		parts := strings.SplitN(id, "/", 2)
		cid := parts[0]
		action := parts[1]

		switch action {
		case "start":
			s.containerAction(w, cid, "start")
		case "stop":
			s.containerAction(w, cid, "stop")
		case "restart":
			s.containerAction(w, cid, "restart")
		case "remove":
			s.containerRemove(w, cid, r)
		case "logs":
			s.containerLogs(w, cid, r)
		case "stats":
			s.containerStats(w, cid)
		case "state":
			s.containerState(w, cid)
		case "exec":
			s.containerExec(w, cid, r)
		case "console":
			s.containerConsole(w, r, cid)
		case "files":
			s.handleFiles(w, r, cid)
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

	if follow {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		s.dckStream(w, args...)
		return
	}

	out, err := s.dck(args...)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"logs": out})
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
	state, err := s.readContainerState(id)
	if err != nil {
		out, err2 := s.dck("ps", "-a")
		if err2 != nil {
			json.NewEncoder(w).Encode(map[string]string{"error": err2.Error()})
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
		return
	}

	json.NewEncoder(w).Encode(state)
}

func (s *Server) containerExec(w http.ResponseWriter, id string, r *http.Request) {
	var req struct {
		Cmd []string `json:"cmd"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
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

// --- Console (WebSocket) ---

func (s *Server) containerConsole(w http.ResponseWriter, r *http.Request, id string) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	shell := r.URL.Query().Get("shell")
	allowedShells := map[string]bool{
		"/bin/sh": true, "/bin/bash": true, "/bin/zsh": true,
		"/bin/dash": true, "/bin/ash": true, "/bin/ksh": true,
	}
	if !allowedShells[shell] {
		shell = "/bin/sh"
	}

	cmd := exec.Command(s.cfg.DckBin, "exec", "-i", "-t", id, shell)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("Error creating stdin pipe: "+err.Error()+"\n"))
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("Error creating stdout pipe: "+err.Error()+"\n"))
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("Error creating stderr pipe: "+err.Error()+"\n"))
		return
	}

	if err := cmd.Start(); err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()+"\n"))
		return
	}

	var wsMu sync.Mutex
	wsWrite := func(typ int, data []byte) error {
		wsMu.Lock()
		defer wsMu.Unlock()
		return conn.WriteMessage(typ, data)
	}

	done := make(chan struct{})

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				wsWrite(websocket.TextMessage, buf[:n])
			}
			if err != nil {
				break
			}
		}
		close(done)
	}()

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				wsWrite(websocket.TextMessage, buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				cmd.Process.Kill()
				return
			}
			stdin.Write(msg)
		}
	}()

	<-done
	cmd.Wait()
}

// --- File Manager ---

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request, id string) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/"
	}
	cleanPath := filepath.Clean("/" + path)
	if !strings.HasPrefix(cleanPath, "/") {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	path = cleanPath

	switch r.Method {
	case "GET":
		action := r.URL.Query().Get("action")
		switch action {
		case "read":
			s.fileRead(w, id, path)
		default:
			s.fileList(w, id, path)
		}
	case "POST":
		action := r.URL.Query().Get("action")
		switch action {
		case "write":
			s.fileWrite(w, r, id, path)
		case "mkdir":
			s.fileMkdir(w, id, path)
		case "rename":
			s.fileRename(w, r, id)
		case "upload":
			s.fileUpload(w, r, id, path)
		default:
			http.Error(w, "Unknown action", http.StatusBadRequest)
		}
	case "DELETE":
		s.fileDelete(w, id, path)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

type FileInfo struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"is_dir"`
	Mode    string `json:"mode"`
	ModTime string `json:"mod_time"`
}

func (s *Server) fileList(w http.ResponseWriter, id, path string) {
	// Use find with -printf for reliable output (no parsing issues)
	out, err := s.dck("exec", id, "sh", "-c", `find "$1" -mindepth 1 -maxdepth 1 -printf '%M|%s|%T@|%p\0' 2>/dev/null || ls -1 "$1"`, "--", path)
	if err != nil {
		// Fallback: return empty
		json.NewEncoder(w).Encode([]FileInfo{})
		return
	}

	var files []FileInfo
	for _, entry := range strings.Split(out, "\x00") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, "|", 4)
		if len(parts) < 4 {
			// Old ls -1 fallback: just names
			files = append(files, FileInfo{Name: entry, IsDir: false})
			continue
		}
		size, _ := strconv.ParseInt(parts[1], 10, 64)
		modSecs, _ := strconv.ParseFloat(parts[2], 64)
		modTime := time.Unix(int64(modSecs), 0).Format("2006-01-02T15:04:05")
		files = append(files, FileInfo{
			Mode:    parts[0],
			Size:    size,
			ModTime: modTime,
			Name:    filepath.Base(parts[3]),
			IsDir:   strings.HasPrefix(parts[0], "d"),
		})
	}
	json.NewEncoder(w).Encode(files)
}

func (s *Server) fileRead(w http.ResponseWriter, id, path string) {
	out, err := s.dck("exec", id, "cat", path)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(out))
}

func (s *Server) fileWrite(w http.ResponseWriter, r *http.Request, id, path string) {
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	dir := filepath.Dir(path)
	s.dck("exec", id, "mkdir", "-p", dir)

	out, err := s.dckWithStdin(
		strings.NewReader(string(body)),
		"exec", id, "sh", "-c", `cat > "$1"`, "--", path,
	)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("%s: %s", err.Error(), out)})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "written"})
}

func (s *Server) fileMkdir(w http.ResponseWriter, id, path string) {
	_, err := s.dck("exec", id, "mkdir", "-p", path)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "created"})
}

func (s *Server) fileRename(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		OldPath string `json:"old_path"`
		NewPath string `json:"new_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	s.dck("exec", id, "mkdir", "-p", filepath.Dir(req.NewPath))
	_, err := s.dck("exec", id, "mv", req.OldPath, req.NewPath)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "renamed"})
}

func (s *Server) fileDelete(w http.ResponseWriter, id, path string) {
	_, err := s.dck("exec", id, "rm", "-rf", path)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}

func (s *Server) fileUpload(w http.ResponseWriter, r *http.Request, id, path string) {
	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	s.dck("exec", id, "mkdir", "-p", filepath.Dir(path))
	out, err := s.dckWithStdin(
		file,
		"exec", id, "sh", "-c", `cat > "$1"`, "--", path,
	)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("%s: %s", err.Error(), out)})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "uploaded"})
}

// --- Image Management ---

func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		out, err := s.dck("images")
		if err != nil {
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		var images []string
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "REPOSITORY") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				images = append(images, fields[0]+":"+fields[1])
			}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"images": images})

	case "POST":
		var req struct {
			Image string `json:"image"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if req.Image == "" {
			http.Error(w, "image is required", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if err := s.dckStream(w, "pull", req.Image); err != nil {
			fmt.Fprintf(w, "\nError: %v\n", err)
			return
		}

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleImageByRef(w http.ResponseWriter, r *http.Request) {
	ref := strings.TrimPrefix(r.URL.Path, "/api/images/")
	ref = strings.TrimSuffix(ref, "/")
	if ref == "" {
		http.Error(w, "Image reference required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case "DELETE":
		_, err := s.dck("rmi", ref)
		if err != nil {
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "removed", "image": ref})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- Bulk Operations ---

func (s *Server) handleSystemPrune(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	out, err := s.dck("system", "prune")
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"output": out})
}

func (s *Server) handleStopAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	home := s.cfg.DataDir
	cdir := filepath.Join(home, "containers")
	entries, err := os.ReadDir(cdir)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"stopped": 0})
		return
	}

	var stopped int
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		state, err := s.readContainerState(id)
		if err != nil {
			continue
		}
		if status, ok := state["status"].(string); ok && status == "running" {
			s.dck("stop", id)
			stopped++
		}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"stopped": stopped})
}
