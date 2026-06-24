package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{
		Port:       0,
		Host:       "127.0.0.1",
		APIKey:     "test-key-123",
		DckBin:     "/usr/local/bin/dck",
		DataDir:    "/tmp/dck-wings-test",
		LogDir:     "",
		DckTimeout: 10,
	}
}

func TestNew(t *testing.T) {
	cfg := testConfig()
	s := New(cfg)
	if s == nil {
		t.Fatal("New() returned nil")
	}
	if s.cfg.APIKey != "test-key-123" {
		t.Errorf("APIKey = %q", s.cfg.APIKey)
	}
}

func TestHealthEndpoint(t *testing.T) {
	cfg := testConfig()
	s := New(cfg)

	req := httptest.NewRequest("GET", "/api/health", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	w := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /api/health returned %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("json decode: %v", err)
	}

	if resp["status"] != "ok" {
		t.Errorf("status = %v, want ok", resp["status"])
	}
	if resp["version"] != "1.5.0" {
		t.Errorf("version = %v", resp["version"])
	}
}

func TestHealthWithoutAuth(t *testing.T) {
	cfg := testConfig()
	s := New(cfg)

	req := httptest.NewRequest("GET", "/api/health", nil)
	w := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestHealthWrongAuth(t *testing.T) {
	cfg := testConfig()
	s := New(cfg)

	req := httptest.NewRequest("GET", "/api/health", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	w := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestRateLimiter(t *testing.T) {
	rl := newRateLimiter(3, time.Second)

	// First 3 requests should be allowed
	for i := 0; i < 3; i++ {
		if !rl.Allow("1.2.3.4") {
			t.Errorf("request %d should be allowed", i+1)
		}
	}

	// 4th request should be blocked
	if rl.Allow("1.2.3.4") {
		t.Error("4th request should be blocked")
	}

	// Different IP should be allowed
	if !rl.Allow("5.6.7.8") {
		t.Error("different IP should be allowed")
	}
}

func TestMethodNotAllowed(t *testing.T) {
	cfg := testConfig()
	s := New(cfg)

	tests := []struct {
		method string
		path   string
	}{
		{"GET", "/api/bootstrap"},
		{"PUT", "/api/containers"},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		req.Header.Set("Authorization", "Bearer test-key-123")
		w := httptest.NewRecorder()
		s.server.Handler.ServeHTTP(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s returned %d, want 405", tt.method, tt.path, w.Code)
		}
	}
}

func TestContainerIDWithBackslashEncoded(t *testing.T) {
	cfg := testConfig()
	s := New(cfg)

	req := httptest.NewRequest("GET", "/api/containers/a%5Cb/start", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	w := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("ID with backslash should be rejected, got %d", w.Code)
	}
}

func TestContainerActionRouting(t *testing.T) {
	cfg := testConfig()
	s := New(cfg)

	req := httptest.NewRequest("POST", "/api/containers/abcd1234/start", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	w := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(w, req)

	if w.Code == http.StatusBadRequest {
		t.Error("valid container action should not be rejected")
	}
}

func TestHandleContainersUnknownAction(t *testing.T) {
	cfg := testConfig()
	s := New(cfg)

	req := httptest.NewRequest("GET", "/api/containers/test-id/nonexistent", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	w := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestBootstrapMethodValidation(t *testing.T) {
	cfg := testConfig()
	s := New(cfg)

	req := httptest.NewRequest("GET", "/api/bootstrap", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	w := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestSystemPruneMethodValidation(t *testing.T) {
	cfg := testConfig()
	s := New(cfg)

	req := httptest.NewRequest("GET", "/api/system/prune", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	w := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestStopAllMethodValidation(t *testing.T) {
	cfg := testConfig()
	s := New(cfg)

	req := httptest.NewRequest("GET", "/api/system/stop-all", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	w := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestImagesList(t *testing.T) {
	cfg := testConfig()
	s := New(cfg)

	req := httptest.NewRequest("GET", "/api/images", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	w := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestImagesPostNoBody(t *testing.T) {
	cfg := testConfig()
	s := New(cfg)

	req := httptest.NewRequest("POST", "/api/images", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	w := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestFileWriteBodyLimit(t *testing.T) {
	cfg := testConfig()
	s := New(cfg)

	bigBody := strings.NewReader(string(make([]byte, 11<<20))) // 11MB (limit is 10MB)
	req := httptest.NewRequest("POST", "/api/containers/test-id/files?action=write&path=/test.txt", bigBody)
	req.Header.Set("Authorization", "Bearer test-key-123")
	w := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for oversized body, got %d", w.Code)
	}
}

func TestExecNoCmd(t *testing.T) {
	cfg := testConfig()
	s := New(cfg)

	body := strings.NewReader(`{}`)
	req := httptest.NewRequest("POST", "/api/containers/test-id/exec", body)
	req.Header.Set("Authorization", "Bearer test-key-123")
	w := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestCreateContainerNoImage(t *testing.T) {
	cfg := testConfig()
	s := New(cfg)

	body := strings.NewReader(`{"name":"test"}`)
	req := httptest.NewRequest("POST", "/api/containers", body)
	req.Header.Set("Authorization", "Bearer test-key-123")
	w := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}
