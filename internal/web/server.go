package web

import (
	"embed"
	"encoding/json"
	"net/http"
	"runtime"
	"strconv"
	"time"

	"github.com/elys-network/avm/internal/logger"
	"github.com/elys-network/avm/internal/state"
	"github.com/gorilla/mux"
)

var webLogger = logger.GetForComponent("web_server")

//go:embed static/*
var staticFiles embed.FS

//go:embed static/index.html
var dashboardHTML []byte

// WebServer handles HTTP requests for vault data visualization
type WebServer struct {
	router *mux.Router
	port   string
}

// NewWebServer creates a new web server instance
func NewWebServer(port string) *WebServer {
	if port == "" {
		port = "8080"
	}

	server := &WebServer{
		router: mux.NewRouter(),
		port:   port,
	}

	server.setupRoutes()
	return server
}

// setupRoutes configures all HTTP routes
func (ws *WebServer) setupRoutes() {
	// Static files
	staticHandler := http.FileServer(http.FS(staticFiles))
	ws.router.PathPrefix("/static/").Handler(http.StripPrefix("/", staticHandler))

	// Dashboard routes
	ws.router.HandleFunc("/", ws.handleDashboard).Methods("GET")
	ws.router.HandleFunc("/dashboard", ws.handleDashboard).Methods("GET")

	// Health endpoint (direct route)
	ws.router.HandleFunc("/health", ws.handleHealth).Methods("GET")

	// API endpoints
	api := ws.router.PathPrefix("/api").Subrouter()
	api.HandleFunc("/health", ws.handleHealth).Methods("GET")
	api.HandleFunc("/cycles", ws.handleGetCycles).Methods("GET")
	api.HandleFunc("/cycles/{id}", ws.handleGetCycle).Methods("GET")
	api.HandleFunc("/cycles/latest", ws.handleGetLatestCycle).Methods("GET")
	api.HandleFunc("/scoring-parameters", ws.handleGetScoringParameters).Methods("GET")
	api.HandleFunc("/vault/summary", ws.handleGetVaultSummary).Methods("GET")
	api.HandleFunc("/performance", ws.handleGetPerformanceMetrics).Methods("GET")

	// Add CORS middleware
	ws.router.Use(ws.corsMiddleware)
	ws.router.Use(ws.loggingMiddleware)
}

// Start starts the web server
func (ws *WebServer) Start() error {
	webLogger.Info().Str("port", ws.port).Msg("Starting web server")

	server := &http.Server{
		Addr:         ":" + ws.port,
		Handler:      ws.router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	return server.ListenAndServe()
}

// handleHealth returns comprehensive server health status
func (ws *WebServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	// Get runtime memory stats
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	
	// Get latest cycle information
	latestCycle, cycleErr := state.GetRecentCycles(1)
	var cycleInfo map[string]interface{}
	var hasErrors bool
	var lastCycleTime *time.Time
	
	if cycleErr == nil && len(latestCycle) > 0 {
		cycle := latestCycle[0]
		cycleInfo = map[string]interface{}{
			"current_cycle":     cycle.CycleNumber,
			"last_cycle_time":   cycle.Timestamp,
			"last_cycle_status": "completed", // CycleSnapshot doesn't have status field, assume completed if exists
			"actions_executed":  len(cycle.ActionReceipts),
		}
		
		// Check if there were any errors in the last cycle
		// We can infer errors from missing transaction hashes or empty action receipts
		hasErrors = len(cycle.TransactionHashes) == 0 && len(cycle.ActionReceipts) > 0
		lastCycleTime = &cycle.Timestamp
	} else {
		cycleInfo = map[string]interface{}{
			"current_cycle":     0,
			"last_cycle_time":   nil,
			"last_cycle_status": "unknown",
			"actions_executed":  0,
		}
		hasErrors = true // No cycle data available indicates an issue
	}
	
	// Get database connection status
	dbHealthy := true
	dbErr := state.TestDBConnection()
	if dbErr != nil {
		dbHealthy = false
		hasErrors = true
	}
	
	// Determine overall status
	overallStatus := "OK"
	if hasErrors {
		overallStatus = "DEGRADED"
	}
	
	// Calculate uptime approximation based on last cycle time
	var uptimeSeconds int64
	if lastCycleTime != nil {
		uptimeSeconds = int64(time.Since(*lastCycleTime).Seconds())
	}
	
	response := map[string]interface{}{
		"status":    overallStatus,
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"system": map[string]interface{}{
			"version":             runtime.Version(),
			"goroutines_count":    runtime.NumGoroutine(),
			"total_alloc_bytes":   memStats.TotalAlloc,
			"heap_objects_count":  memStats.HeapObjects,
			"alloc_bytes":         memStats.Alloc,
			"sys_bytes":           memStats.Sys,
			"gc_cycles":           memStats.NumGC,
			"uptime_seconds":      uptimeSeconds,
		},
		"component": map[string]interface{}{
			"name":    "avm-autonomous-vault-manager",
			"version": "1.0.0",
		},
		"avm_status": map[string]interface{}{
			"database_healthy":    dbHealthy,
			"has_recent_errors":   hasErrors,
			"cycle_info":          cycleInfo,
		},
	}

	// Set appropriate HTTP status code
	statusCode := http.StatusOK
	if hasErrors {
		statusCode = http.StatusServiceUnavailable
	}

	ws.writeJSONResponse(w, statusCode, response)
}

// handleDashboard serves the main dashboard HTML
func (ws *WebServer) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.WriteHeader(http.StatusOK)
	w.Write(dashboardHTML)
}

// handleGetCycles returns paginated cycle data
func (ws *WebServer) handleGetCycles(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if parsedLimit, err := strconv.Atoi(limitStr); err == nil && parsedLimit > 0 && parsedLimit <= 100 {
			limit = parsedLimit
		}
	}

	cycles, err := state.GetRecentCycles(limit)
	if err != nil {
		webLogger.Error().Err(err).Msg("Failed to get recent cycles")
		ws.writeErrorResponse(w, http.StatusInternalServerError, "Failed to retrieve cycles")
		return
	}

	response := map[string]interface{}{
		"cycles": cycles,
		"count":  len(cycles),
		"limit":  limit,
	}

	ws.writeJSONResponse(w, http.StatusOK, response)
}

// handleGetCycle returns a specific cycle by ID
func (ws *WebServer) handleGetCycle(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	idStr := vars["id"]

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		ws.writeErrorResponse(w, http.StatusBadRequest, "Invalid cycle ID")
		return
	}

	cycle, err := state.GetCycleByID(id)
	if err != nil {
		webLogger.Error().Err(err).Int64("cycleId", id).Msg("Failed to get cycle")
		ws.writeErrorResponse(w, http.StatusNotFound, "Cycle not found")
		return
	}

	ws.writeJSONResponse(w, http.StatusOK, cycle)
}

// handleGetLatestCycle returns the most recent cycle
func (ws *WebServer) handleGetLatestCycle(w http.ResponseWriter, r *http.Request) {
	cycles, err := state.GetRecentCycles(1)
	if err != nil || len(cycles) == 0 {
		webLogger.Error().Err(err).Msg("Failed to get latest cycle")
		ws.writeErrorResponse(w, http.StatusNotFound, "No cycles found")
		return
	}

	ws.writeJSONResponse(w, http.StatusOK, cycles[0])
}

// handleGetScoringParameters returns current scoring parameters
func (ws *WebServer) handleGetScoringParameters(w http.ResponseWriter, r *http.Request) {
	params, err := state.LoadActiveScoringParameters("default_avm_strategy")
	if err != nil {
		webLogger.Error().Err(err).Msg("Failed to get scoring parameters")
		ws.writeErrorResponse(w, http.StatusInternalServerError, "Failed to retrieve scoring parameters")
		return
	}

	response := map[string]interface{}{
		"parameters": params,
		"timestamp":  time.Now().UTC(),
	}

	ws.writeJSONResponse(w, http.StatusOK, response)
}

// handleGetVaultSummary returns vault summary statistics
func (ws *WebServer) handleGetVaultSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := state.GetVaultSummary()
	if err != nil {
		webLogger.Error().Err(err).Msg("Failed to get vault summary")
		ws.writeErrorResponse(w, http.StatusInternalServerError, "Failed to retrieve vault summary")
		return
	}

	ws.writeJSONResponse(w, http.StatusOK, summary)
}

// handleGetPerformanceMetrics returns performance metrics
func (ws *WebServer) handleGetPerformanceMetrics(w http.ResponseWriter, r *http.Request) {
	metrics, err := state.GetPerformanceMetrics()
	if err != nil {
		webLogger.Error().Err(err).Msg("Failed to get performance metrics")
		ws.writeErrorResponse(w, http.StatusInternalServerError, "Failed to retrieve performance metrics")
		return
	}

	ws.writeJSONResponse(w, http.StatusOK, metrics)
}

// writeJSONResponse writes a JSON response
func (ws *WebServer) writeJSONResponse(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	if err := json.NewEncoder(w).Encode(data); err != nil {
		webLogger.Error().Err(err).Msg("Failed to encode JSON response")
	}
}

// writeErrorResponse writes an error response
func (ws *WebServer) writeErrorResponse(w http.ResponseWriter, statusCode int, message string) {
	response := map[string]interface{}{
		"error":     true,
		"message":   message,
		"timestamp": time.Now().UTC(),
	}

	ws.writeJSONResponse(w, statusCode, response)
}

// corsMiddleware adds CORS headers
func (ws *WebServer) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// loggingMiddleware logs HTTP requests
func (ws *WebServer) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Create a response writer wrapper to capture status code
		wrapper := &responseWriterWrapper{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(wrapper, r)

		duration := time.Since(start)

		webLogger.Info().
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Str("remote_addr", r.RemoteAddr).
			Int("status", wrapper.statusCode).
			Dur("duration", duration).
			Msg("HTTP request")
	})
}

// responseWriterWrapper wraps http.ResponseWriter to capture status code
type responseWriterWrapper struct {
	http.ResponseWriter
	statusCode int
}

func (w *responseWriterWrapper) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}
