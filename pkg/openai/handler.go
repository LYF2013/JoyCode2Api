package openai

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/vibe-coding-labs/JoyCode2Api/pkg/joycode"
	"github.com/vibe-coding-labs/JoyCode2Api/pkg/store"
)

// ClientResolver returns the appropriate joycode.Client for a request.
type ClientResolver func(r *http.Request) *joycode.Client

// Server implements the OpenAI-compatible HTTP API.
type Server struct {
	Client   *joycode.Client
	Resolver ClientResolver
	store    *store.Store

	// model adapter cache: model ID → adapterType, with TTL
	adapterMu       sync.RWMutex
	adapterMap      map[string]string
	adapterFetchedAt time.Time
}

// NewServer creates a new OpenAI-compatible proxy server.
func NewServer(c *joycode.Client, s *store.Store) *Server {
	return &Server{Client: c, store: s}
}

func (s *Server) getClient(r *http.Request) *joycode.Client {
	if s.Resolver != nil {
		return s.Resolver(r)
	}
	return s.Client
}

// getAdapterType returns the adapter type for a model, using a cached model list
// with a 10-minute TTL. On error returns "" (defaults to chat completions).
func (s *Server) getAdapterType(client *joycode.Client, model string) string {
	s.adapterMu.RLock()
	if s.adapterMap != nil && time.Since(s.adapterFetchedAt) < 10*time.Minute {
		at := s.adapterMap[model]
		s.adapterMu.RUnlock()
		return at
	}
	s.adapterMu.RUnlock()

	// Fetch fresh model list
	s.adapterMu.Lock()
	defer s.adapterMu.Unlock()

	// Double-check after acquiring write lock
	if s.adapterMap != nil && time.Since(s.adapterFetchedAt) < 10*time.Minute {
		return s.adapterMap[model]
	}

	models, err := client.ListModels()
	if err != nil {
		slog.Warn("failed to fetch model list for adapter type", "error", err)
		return ""
	}

	m := make(map[string]string, len(models))
	for _, mi := range models {
		mid := mi.ModelID
		if mid == "" {
			mid = mi.Label
		}
		m[mid] = mi.AdapterType()
	}
	s.adapterMap = m
	s.adapterFetchedAt = time.Now()

	return m[model]
}

// RegisterRoutes registers all OpenAI-compatible endpoints on the mux.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v1/chat/completions", s.handleChat)
	mux.HandleFunc("/v1/responses", s.handleResponses)
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/v1/web-search", s.handleWebSearch)
	mux.HandleFunc("/v1/rerank", s.handleRerank)
	mux.HandleFunc("/health", s.handleHealth)
}

func writeCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	b, err := json.Marshal(v)
	if err != nil {
		slog.Error("writeJSON: marshal failed", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(code)
	w.Write(b)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]interface{}{
		"error": map[string]string{"message": msg, "type": "api_error"},
	})
}

func requirePOST(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodOptions {
		writeCORS(w)
		w.WriteHeader(200)
		return false
	}
	if r.Method != http.MethodPost {
		writeError(w, 405, "method not allowed")
		return false
	}
	return true
}

func requireGET(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodOptions {
		writeCORS(w)
		w.WriteHeader(200)
		return false
	}
	return true
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		writeCORS(w)
		w.WriteHeader(200)
		return
	}
	writeJSON(w, 200, map[string]interface{}{
		"status": "ok", "service": "joycode-openai-proxy",
		"endpoints": []string{
			"/v1/chat/completions", "/v1/responses", "/v1/models",
			"/v1/web-search", "/v1/rerank",
		},
	})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if !requireGET(w, r) {
		return
	}
	models, err := s.getClient(r).ListModels()
	if err != nil {
		slog.Error("list models upstream error", "error", err)
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, TranslateModels(models))
}
