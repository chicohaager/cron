package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	conf "github.com/chicohaager/cron/internal/config"
	cronpkg "github.com/chicohaager/cron/internal/cron"
	"github.com/chicohaager/cron/internal/notify"
	svc "github.com/chicohaager/cron/internal/service"
	"github.com/chicohaager/cron/internal/storage"
)

type Task struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Command    string        `json:"command"`
	Type       string        `json:"type"`
	Interval   time.Duration `json:"interval_ms"`
	CronExpr   string        `json:"cron_expr"`
	Status     string        `json:"status"`
	NextRunAt  int64         `json:"next_run_at"`
	LastRunAt  int64         `json:"last_run_at"`
	LastResult *Result       `json:"last_result"`

	// Phase 2: Timeout, Retry, Environment
	TimeoutSec    int               `json:"timeout_sec"`
	RetryCount    int               `json:"retry_count"`
	RetryDelaySec int               `json:"retry_delay_sec"`
	CurrentRetry  int               `json:"current_retry"`
	Env           map[string]string `json:"env,omitempty"`

	// Phase 3: Notifications
	Notifications []notify.Config `json:"notifications,omitempty"`

	// Phase 4: Categories and Tags
	Category string   `json:"category,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Priority int      `json:"priority,omitempty"`

	// Phase 5: Dependencies
	DependsOn     []string `json:"depends_on,omitempty"`
	AllowParallel bool     `json:"allow_parallel,omitempty"`

	// Phase 6: Log Management
	MaxLogEntries int `json:"max_log_entries,omitempty"`

	// Runtime state
	Executing bool `json:"executing"`

	logs   []LogEntry
	timer  *time.Timer
	ticker *time.Ticker
	done   chan struct{}
}

type Result struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}
type LogEntry struct {
	Time       int64  `json:"time"`
	DurationMs int64  `json:"duration_ms"`
	Success    bool   `json:"success"`
	Message    string `json:"message"`
}

const (
	defaultStoragePath = "/DATA/AppData/cron"
	version            = "0.2.0"
	maxRequestBody     = 1 << 20 // 1 MB
	maxTasks           = 500
)

var (
	tasks     = map[string]*Task{}
	mu        sync.RWMutex
	store     storage.Storage
	startTime = time.Now()
	execSem   = make(chan struct{}, 10) // max 10 concurrent executions
)

func installWatchdog() {
	// Only install on systems with systemd (e.g. ZimaOS), skip on dev/CI
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		log.Printf("[cron] Systemd not detected, skipping watchdog install")
		return
	}

	units := map[string]string{
		"/etc/systemd/system/cron-watchdog.service": `[Unit]
Description=Restart cron if not running

[Service]
Type=oneshot
ExecStart=/bin/sh -c 'systemctl is-active cron.service || systemctl start cron.service'
`,
		"/etc/systemd/system/cron-watchdog.timer": `[Unit]
Description=Ensure cron is running after sysext refresh

[Timer]
OnBootSec=15

[Install]
WantedBy=timers.target
`,
		"/etc/systemd/system/cron-refresh.path": `[Unit]
Description=Watch cron binary for updates

[Path]
PathChanged=/usr/bin/cron

[Install]
WantedBy=multi-user.target
`,
		"/etc/systemd/system/cron-refresh.service": `[Unit]
Description=Restart cron after binary update

[Service]
Type=oneshot
ExecStart=/bin/sh -c 'sleep 2 && systemctl restart cron.service'
`,
	}

	changed := false
	for path, content := range units {
		if existing, err := os.ReadFile(path); err == nil && string(existing) == content {
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			log.Printf("[cron] Could not write %s: %v", path, err)
			return
		}
		changed = true
	}
	if !changed {
		return
	}

	if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
		log.Printf("[cron] systemctl daemon-reload: %v", err)
	}
	for _, unit := range []string{"cron-watchdog.timer", "cron-refresh.path"} {
		if err := exec.Command("systemctl", "enable", "--now", unit).Run(); err != nil {
			log.Printf("[cron] systemctl enable %s: %v", unit, err)
		}
	}
	log.Printf("[cron] Watchdog timer and refresh path unit installed")
}

func main() {
	log.Printf("[cron] Starting cron backend...")

	// Ensure watchdog timer is installed (survives sysext refresh)
	installWatchdog()

	// Initialize persistent storage (retry until data path is available)
	storagePath := defaultStoragePath
	if envPath := os.Getenv("CRON_DATA_PATH"); envPath != "" {
		storagePath = envPath
	}
	var fs *storage.FileStorage
	for i := 0; i < 30; i++ {
		var err error
		fs, err = storage.NewFileStorage(storagePath)
		if err == nil {
			break
		}
		log.Printf("[cron] Storage not ready (attempt %d/30): %v", i+1, err)
		time.Sleep(2 * time.Second)
	}
	if fs == nil {
		log.Fatalf("[cron] Failed to initialize storage at %s after 30 attempts", storagePath)
	}
	store = fs

	// Load persisted tasks
	if err := loadPersistedTasks(); err != nil {
		log.Printf("[cron] Warning: failed to load tasks: %v", err)
	}

	runtimePath := conf.CommonInfo.RuntimePath
	if envPath := os.Getenv("CASAOS_RUNTIME_PATH"); envPath != "" {
		runtimePath = envPath
		log.Printf("[cron] Overriding runtime path to: %s", runtimePath)
	}

	// Start HTTP server first, then register gateway route asynchronously.
	// This ensures the server is always available, even if the gateway is slow to start.
	mux := http.NewServeMux()
	mux.HandleFunc("/cron/tasks", withLogging(withCORS(tasksHandler)))
	mux.HandleFunc("/cron/tasks/", withLogging(withCORS(taskActionHandler)))
	mux.HandleFunc("/cron/categories", withLogging(withCORS(categoriesHandler)))
	mux.HandleFunc("/cron/tags", withLogging(withCORS(tagsHandler)))
	mux.HandleFunc("/cron/cron/validate", withLogging(withCORS(cronValidateHandler)))
	mux.HandleFunc("/cron/tasks/bulk/run", withLogging(withCORS(bulkRunHandler)))
	mux.HandleFunc("/cron/tasks/bulk/toggle", withLogging(withCORS(bulkToggleHandler)))
	mux.HandleFunc("/cron/tasks/bulk/delete", withLogging(withCORS(bulkDeleteHandler)))
	mux.HandleFunc("/cron/export", withLogging(withCORS(exportHandler)))
	mux.HandleFunc("/cron/import", withLogging(withCORS(importHandler)))
	mux.HandleFunc("/cron/health", withLogging(withCORS(healthHandler)))
	mux.HandleFunc("/cron/templates", withLogging(withCORS(templatesHandler)))
	mux.HandleFunc("/cron/settings", withLogging(withCORS(settingsHandler)))
	mux.HandleFunc("/cron/settings/test-telegram", withLogging(withCORS(testTelegramHandler)))
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", "0"))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("cron backend listening on http://%s", listener.Addr().String())

	// Register gateway route asynchronously — retries if gateway isn't ready yet.
	// This never blocks the HTTP server from serving requests.
	log.Printf("[cron] Gateway runtime path: %q", runtimePath)
	svc.RegisterRouteAsync(runtimePath, "/cron", "http://"+listener.Addr().String())

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	// Graceful shutdown on SIGTERM/SIGINT
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Printf("[cron] Received %v, shutting down...", sig)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Stop all task schedules
		mu.Lock()
		for _, t := range tasks {
			clearSchedule(t)
		}
		mu.Unlock()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("[cron] Shutdown error: %v", err)
		}
	}()

	if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	log.Printf("[cron] Server stopped")
}

func withCORS(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h(w, r)
	}
}

func jsonResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
}

func withLogging(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		log.Printf("[REQ] %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
		// Apply body size limit to all mutating requests
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		}
		h(w, r)
		log.Printf("[RES] %s %s took %v", r.Method, r.URL.Path, time.Since(start))
	}
}

func newTaskID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10) // fallback
	}
	return hex.EncodeToString(b)
}

type createReq struct {
	Name          string            `json:"name"`
	Command       string            `json:"command"`
	Type          string            `json:"type"`
	IntervalMin   int               `json:"interval_min"`
	CronExpr      string            `json:"cron_expr"`
	TimeoutSec    int               `json:"timeout_sec"`
	RetryCount    int               `json:"retry_count"`
	RetryDelaySec int               `json:"retry_delay_sec"`
	Env           map[string]string `json:"env,omitempty"`
	Notifications []notify.Config   `json:"notifications,omitempty"`
	Category      string            `json:"category,omitempty"`
	Tags          []string          `json:"tags,omitempty"`
	Priority      int               `json:"priority,omitempty"`
	DependsOn     []string          `json:"depends_on,omitempty"`
	AllowParallel bool              `json:"allow_parallel,omitempty"`
	MaxLogEntries int               `json:"max_log_entries,omitempty"`
}

func tasksHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		categoryFilter := r.URL.Query().Get("category")
		tagFilter := r.URL.Query().Get("tag")
		mu.RLock()
		out := make([]*Task, 0, len(tasks))
		for _, t := range tasks {
			if categoryFilter != "" && t.Category != categoryFilter {
				continue
			}
			if tagFilter != "" && !hasTag(t.Tags, tagFilter) {
				continue
			}
			out = append(out, sanitizeTask(t))
		}
		mu.RUnlock()
		jsonResponse(w)
		json.NewEncoder(w).Encode(out)
	case http.MethodPost:
		mu.RLock()
		taskCount := len(tasks)
		mu.RUnlock()
		if taskCount >= maxTasks {
			http.Error(w, fmt.Sprintf("task limit reached (%d)", maxTasks), 429)
			return
		}
		var req createReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Command) == "" {
			http.Error(w, "name/command required", 400)
			return
		}
		if req.Type != "interval" && req.Type != "cron" {
			http.Error(w, "invalid type", 400)
			return
		}
		// Validate notification configs
		for _, n := range req.Notifications {
			if n.Type != "webhook" && n.Type != "email" && n.Type != "telegram" {
				http.Error(w, "invalid notification type: "+n.Type, 400)
				return
			}
		}
		id := newTaskID()
		t := &Task{
			ID: id, Name: req.Name, Command: req.Command, Type: req.Type,
			Status: "running", CronExpr: "",
			TimeoutSec: req.TimeoutSec, RetryCount: req.RetryCount,
			RetryDelaySec: req.RetryDelaySec, Env: req.Env,
			Notifications: req.Notifications,
			Category: req.Category, Tags: req.Tags, Priority: req.Priority,
			DependsOn: req.DependsOn, AllowParallel: req.AllowParallel,
			MaxLogEntries: req.MaxLogEntries,
		}
		if req.Type == "interval" {
			if req.IntervalMin < 1 {
				http.Error(w, "interval_min >=1", 400)
				return
			}
			t.Interval = time.Duration(req.IntervalMin) * time.Minute
		} else {
			if !isValidCron(req.CronExpr) {
				http.Error(w, "invalid cron", 400)
				return
			}
			t.CronExpr = req.CronExpr
		}
		mu.Lock()
		tasks[id] = t
		mu.Unlock()
		startSchedule(t)
		persistTask(t)
		jsonResponse(w)
		json.NewEncoder(w).Encode(sanitizeTask(t))
	default:
		w.WriteHeader(405)
	}
}

func taskActionHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/cron/tasks/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		w.WriteHeader(404)
		return
	}
	id := parts[0]
	mu.RLock()
	t := tasks[id]
	mu.RUnlock()
	if t == nil {
		w.WriteHeader(404)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		jsonResponse(w)
		json.NewEncoder(w).Encode(sanitizeTask(t))
		return
	}
	if len(parts) == 1 && r.Method == http.MethodDelete {
		mu.Lock()
		clearSchedule(t)
		delete(tasks, id)
		mu.Unlock()
		persistDelete(id)
		w.WriteHeader(204)
		return
	}
	if len(parts) < 2 {
		w.WriteHeader(404)
		return
	}
	action := parts[1]
	switch action {
	case "run":
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		// Non-blocking: run in goroutine so HTTP handler doesn't block on semaphore (M1)
		go func() {
			runTaskOnce(t)
			persistTask(t)
		}()
		jsonResponse(w)
		json.NewEncoder(w).Encode(map[string]string{"status": "triggered"})
	case "toggle":
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		toggleTask(t)
		persistTask(t)
		jsonResponse(w)
		json.NewEncoder(w).Encode(sanitizeTask(t))
	case "logs":
		if r.Method == http.MethodGet {
			mu.RLock()
			logs := append([]LogEntry(nil), t.logs...)
			mu.RUnlock()
			if logs == nil {
				logs = []LogEntry{}
			}
			// Filter by time range
			if fromStr := r.URL.Query().Get("from"); fromStr != "" {
				if fromTs, err := strconv.ParseInt(fromStr, 10, 64); err == nil {
					filtered := logs[:0]
					for _, l := range logs {
						if l.Time >= fromTs {
							filtered = append(filtered, l)
						}
					}
					logs = filtered
				}
			}
			if toStr := r.URL.Query().Get("to"); toStr != "" {
				if toTs, err := strconv.ParseInt(toStr, 10, 64); err == nil {
					filtered := logs[:0]
					for _, l := range logs {
						if l.Time <= toTs {
							filtered = append(filtered, l)
						}
					}
					logs = filtered
				}
			}
			// Filter by search term
			if search := r.URL.Query().Get("search"); search != "" {
				searchLower := strings.ToLower(search)
				filtered := logs[:0]
				for _, l := range logs {
					if strings.Contains(strings.ToLower(l.Message), searchLower) {
						filtered = append(filtered, l)
					}
				}
				logs = filtered
			}
			// Output format
			format := r.URL.Query().Get("format")
			if format == "csv" {
				w.Header().Set("Content-Type", "text/csv")
				w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s_logs.csv", id))
				fmt.Fprintln(w, "time,duration_ms,success,message")
				for _, l := range logs {
					msg := l.Message
					// Strip newlines to prevent CSV cell breakout
					msg = strings.ReplaceAll(msg, "\r", " ")
					msg = strings.ReplaceAll(msg, "\n", " ")
					msg = strings.ReplaceAll(msg, "\t", " ")
					msg = strings.ReplaceAll(msg, "\"", "\"\"")
					// Sanitize CSV injection: prefix dangerous characters with single quote
					if len(msg) > 0 && (msg[0] == '=' || msg[0] == '+' || msg[0] == '-' || msg[0] == '@' || msg[0] == '|') {
						msg = "'" + msg
					}
					fmt.Fprintf(w, "%d,%d,%t,\"%s\"\n", l.Time, l.DurationMs, l.Success, msg)
				}
			} else {
				jsonResponse(w)
				json.NewEncoder(w).Encode(logs)
			}
		} else if r.Method == http.MethodPost && len(parts) >= 3 && parts[2] == "clear" {
			mu.Lock()
			t.logs = nil
			mu.Unlock()
			w.WriteHeader(204)
		} else {
			w.WriteHeader(405)
		}
	default:
		w.WriteHeader(404)
	}
}

func sanitizeTask(t *Task) *Task {
	cp := *t
	cp.logs = nil
	cp.ticker = nil
	cp.timer = nil
	cp.done = nil
	// Mask sensitive fields in notification configs
	if len(cp.Notifications) > 0 {
		masked := make([]notify.Config, len(cp.Notifications))
		copy(masked, cp.Notifications)
		for i := range masked {
			if masked[i].SMTPPass != "" {
				masked[i].SMTPPass = "********"
			}
			if masked[i].TelegramBotToken != "" {
				masked[i].TelegramBotToken = "********"
			}
		}
		cp.Notifications = masked
	}
	return &cp
}

func startSchedule(t *Task) {
	clearSchedule(t)
	if t.Type == "interval" {
		t.ticker = time.NewTicker(t.Interval)
		t.done = make(chan struct{})
		t.NextRunAt = time.Now().Add(t.Interval).UnixMilli()
		go func(id string, done <-chan struct{}) {
			for {
				select {
				case <-done:
					return
				case <-t.ticker.C:
					mu.Lock()
					tt := tasks[id]
					mu.Unlock()
					if tt == nil || tt.Status != "running" {
						continue
					}
					runTaskOnce(tt)
					tt.NextRunAt = time.Now().Add(tt.Interval).UnixMilli()
				}
			}
		}(t.ID, t.done)
	} else {
		scheduleCronNext(t)
	}
}

func clearSchedule(t *Task) {
	if t.done != nil {
		close(t.done)
		t.done = nil
	}
	if t.ticker != nil {
		t.ticker.Stop()
		t.ticker = nil
	}
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
}

func toggleTask(t *Task) {
	if t.Status == "running" {
		t.Status = "paused"
		clearSchedule(t)
		t.NextRunAt = 0
	} else {
		t.Status = "running"
		startSchedule(t)
	}
}

func runTaskOnce(t *Task) {
	// Concurrency limit via non-blocking select with timeout
	select {
	case execSem <- struct{}{}:
	case <-time.After(30 * time.Second):
		log.Printf("[cron] Task %s skipped: execution queue full", t.ID)
		return
	}
	defer func() { <-execSem }()

	// Copy fields under lock for safe access (C1)
	mu.Lock()
	taskID := t.ID
	taskName := t.Name
	taskCommand := t.Command
	taskTimeoutSec := t.TimeoutSec
	taskRetryCount := t.RetryCount
	taskRetryDelaySec := t.RetryDelaySec
	taskCurrentRetry := t.CurrentRetry
	taskMaxLogEntries := t.MaxLogEntries
	taskDependsOn := append([]string(nil), t.DependsOn...)
	taskNotifications := append([]notify.Config(nil), t.Notifications...)
	taskEnv := make(map[string]string, len(t.Env))
	for k, v := range t.Env {
		taskEnv[k] = v
	}
	mu.Unlock()

	// Check dependencies before running (uses copied deps, no lock needed) (C2)
	if !canRunWithDeps(taskDependsOn) {
		log.Printf("[cron] Task %s skipped: dependencies not met", taskID)
		mu.Lock()
		t.LastResult = &Result{Success: false, Message: "Skipped: dependency not met"}
		mu.Unlock()
		persistTask(t)
		return
	}

	mu.Lock()
	t.Executing = true
	mu.Unlock()

	start := time.Now()

	// Configurable timeout (default: 2 minutes)
	timeout := time.Duration(taskTimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/sh", "-lc", taskCommand)

	// Environment variables
	if len(taskEnv) > 0 {
		cmd.Env = os.Environ()
		for k, v := range taskEnv {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	finished := time.Now()
	success := err == nil && ctx.Err() == nil

	var msg string
	if success {
		msg = strings.TrimSpace(stdout.String())
		if msg == "" {
			msg = "Execution completed"
		}
	} else {
		// On failure, combine stdout + stderr for full context
		combined := strings.TrimSpace(stdout.String() + "\n" + stderr.String())
		if combined == "" || combined == "\n" {
			if ctx.Err() == context.DeadlineExceeded {
				combined = fmt.Sprintf("Timeout after %ds", taskTimeoutSec)
			} else if err != nil {
				combined = err.Error()
			}
		}
		msg = strings.TrimSpace(combined)
	}
	if len(msg) > 4000 {
		msg = msg[:4000] + "..."
	}

	durationMs := finished.Sub(start).Milliseconds()

	mu.Lock()
	t.Executing = false
	t.LastRunAt = finished.UnixMilli()
	t.LastResult = &Result{Success: success, Message: msg}
	t.logs = append([]LogEntry{{Time: t.LastRunAt, DurationMs: durationMs, Success: success, Message: msg}}, t.logs...)
	// Log rotation
	maxLogs := taskMaxLogEntries
	if maxLogs <= 0 {
		maxLogs = 100 // default
	}
	if len(t.logs) > maxLogs {
		t.logs = t.logs[:maxLogs]
	}
	// Retry logic — read/write CurrentRetry under lock (C1)
	shouldRetry := false
	if !success && taskRetryCount > 0 && taskCurrentRetry < taskRetryCount {
		t.CurrentRetry++
		shouldRetry = true
	} else {
		t.CurrentRetry = 0
	}
	mu.Unlock()

	if shouldRetry {
		retryDelay := time.Duration(taskRetryDelaySec) * time.Second
		if retryDelay <= 0 {
			retryDelay = 10 * time.Second
		}
		log.Printf("[cron] Task %s failed, retrying %d/%d in %v", taskID, taskCurrentRetry+1, taskRetryCount, retryDelay)
		time.AfterFunc(retryDelay, func() {
			runTaskOnce(t)
		})
	} else {
		// Send notifications only on final result (not during retries)
		taskInfo := notify.TaskInfo{ID: taskID, Name: taskName, Command: taskCommand}
		resultInfo := notify.ResultInfo{Success: success, Message: msg, DurationMs: durationMs}
		if len(taskNotifications) > 0 {
			notify.Send(taskNotifications, taskInfo, resultInfo)
		}
		// Global Telegram notification (from settings)
		if tgCfg := getTelegramNotifyConfig(); tgCfg != nil {
			notify.Send([]notify.Config{*tgCfg}, taskInfo, resultInfo)
		}
	}

	persistTask(t)
}

// taskToData converts an in-memory Task to a persistable TaskData.
func taskToData(t *Task) *storage.TaskData {
	td := &storage.TaskData{
		ID:            t.ID,
		Name:          t.Name,
		Command:       t.Command,
		Type:          t.Type,
		IntervalMs:    int64(t.Interval / time.Millisecond),
		CronExpr:      t.CronExpr,
		Status:        t.Status,
		NextRunAt:     t.NextRunAt,
		LastRunAt:     t.LastRunAt,
		TimeoutSec:    t.TimeoutSec,
		RetryCount:    t.RetryCount,
		RetryDelaySec: t.RetryDelaySec,
		Env:           t.Env,
	}
	if t.LastResult != nil {
		td.LastResult = &storage.ResultData{
			Success: t.LastResult.Success,
			Message: t.LastResult.Message,
		}
	}
	if len(t.Notifications) > 0 {
		if data, err := json.Marshal(t.Notifications); err == nil {
			td.Notifications = data
		}
	}
	td.Category = t.Category
	td.Tags = t.Tags
	td.Priority = t.Priority
	td.DependsOn = t.DependsOn
	td.AllowParallel = t.AllowParallel
	td.MaxLogEntries = t.MaxLogEntries
	// Persist logs
	if len(t.logs) > 0 {
		td.Logs = make([]storage.LogEntryData, len(t.logs))
		for i, l := range t.logs {
			td.Logs[i] = storage.LogEntryData{
				Time: l.Time, DurationMs: l.DurationMs,
				Success: l.Success, Message: l.Message,
			}
		}
	}
	return td
}

// dataToTask converts a persisted TaskData to an in-memory Task.
func dataToTask(td *storage.TaskData) *Task {
	t := &Task{
		ID:            td.ID,
		Name:          td.Name,
		Command:       td.Command,
		Type:          td.Type,
		Interval:      time.Duration(td.IntervalMs) * time.Millisecond,
		CronExpr:      td.CronExpr,
		Status:        td.Status,
		NextRunAt:     td.NextRunAt,
		LastRunAt:     td.LastRunAt,
		TimeoutSec:    td.TimeoutSec,
		RetryCount:    td.RetryCount,
		RetryDelaySec: td.RetryDelaySec,
		Env:           td.Env,
		Category:      td.Category,
		Tags:          td.Tags,
		Priority:      td.Priority,
		DependsOn:     td.DependsOn,
		AllowParallel: td.AllowParallel,
		MaxLogEntries: td.MaxLogEntries,
	}
	if td.LastResult != nil {
		t.LastResult = &Result{
			Success: td.LastResult.Success,
			Message: td.LastResult.Message,
		}
	}
	if len(td.Notifications) > 0 {
		var configs []notify.Config
		if err := json.Unmarshal(td.Notifications, &configs); err == nil {
			t.Notifications = configs
		}
	}
	// Restore logs
	if len(td.Logs) > 0 {
		t.logs = make([]LogEntry, len(td.Logs))
		for i, l := range td.Logs {
			t.logs[i] = LogEntry{
				Time: l.Time, DurationMs: l.DurationMs,
				Success: l.Success, Message: l.Message,
			}
		}
	}
	return t
}

// loadPersistedTasks loads tasks from storage and restarts their schedules.
func loadPersistedTasks() error {
	persisted, err := store.LoadTasks()
	if err != nil {
		return err
	}
	mu.Lock()
	for _, td := range persisted {
		t := dataToTask(td)
		tasks[t.ID] = t
	}
	mu.Unlock()

	// Restart schedules for running tasks
	mu.Lock()
	running := make([]*Task, 0)
	for _, t := range tasks {
		if t.Status == "running" {
			running = append(running, t)
		}
	}
	mu.Unlock()

	for _, t := range running {
		startSchedule(t)
	}
	log.Printf("[cron] Loaded %d tasks from storage (%d running)", len(persisted), len(running))
	return nil
}

// persistTask saves a single task to storage (best-effort, logs errors).
func persistTask(t *Task) {
	if store == nil {
		return
	}
	if err := store.SaveTask(taskToData(t)); err != nil {
		log.Printf("[cron] Error persisting task %s: %v", t.ID, err)
	}
}

// persistDelete removes a task from storage (best-effort, logs errors).
func persistDelete(id string) {
	if store == nil {
		return
	}
	if err := store.DeleteTask(id); err != nil {
		log.Printf("[cron] Error deleting task %s from storage: %v", id, err)
	}
}

// canRunWithDeps checks whether all dependencies have succeeded.
// Takes a pre-copied dependency list to avoid double-locking (C2).
func canRunWithDeps(dependsOn []string) bool {
	if len(dependsOn) == 0 {
		return true
	}
	mu.RLock()
	defer mu.RUnlock()
	for _, depID := range dependsOn {
		dep, ok := tasks[depID]
		if !ok {
			continue // ignore missing dependencies
		}
		if dep.LastResult == nil || !dep.LastResult.Success {
			return false
		}
	}
	return true
}

func hasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

func categoriesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	mu.RLock()
	seen := map[string]bool{}
	cats := []string{}
	for _, t := range tasks {
		if t.Category != "" && !seen[t.Category] {
			seen[t.Category] = true
			cats = append(cats, t.Category)
		}
	}
	mu.RUnlock()
	jsonResponse(w)
	json.NewEncoder(w).Encode(cats)
}

func tagsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	mu.RLock()
	seen := map[string]bool{}
	allTags := []string{}
	for _, t := range tasks {
		for _, tag := range t.Tags {
			if !seen[tag] {
				seen[tag] = true
				allTags = append(allTags, tag)
			}
		}
	}
	mu.RUnlock()
	jsonResponse(w)
	json.NewEncoder(w).Encode(allTags)
}

func isValidCron(expr string) bool {
	_, ok := cronpkg.Validate(expr)
	return ok
}

func cronValidateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	var req struct {
		Expr string `json:"expr"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	errs, valid := cronpkg.Validate(req.Expr)
	resp := struct {
		Valid    bool                      `json:"valid"`
		Errors   []cronpkg.ValidationError `json:"errors"`
		NextRuns []int64                   `json:"next_runs"`
	}{
		Valid:  valid,
		Errors: errs,
	}
	if valid {
		now := time.Now()
		for i := 0; i < 5; i++ {
			next := cronNext(req.Expr, now)
			if next.IsZero() {
				break
			}
			resp.NextRuns = append(resp.NextRuns, next.UnixMilli())
			now = next
		}
	}
	if resp.Errors == nil {
		resp.Errors = []cronpkg.ValidationError{}
	}
	if resp.NextRuns == nil {
		resp.NextRuns = []int64{}
	}
	jsonResponse(w)
	json.NewEncoder(w).Encode(resp)
}

// --- Phase 8: Bulk Operations ---

type bulkReq struct {
	IDs []string `json:"ids"`
}

func bulkRunHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	var req bulkReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	mu.RLock()
	var toRun []*Task
	for _, id := range req.IDs {
		if t, ok := tasks[id]; ok {
			toRun = append(toRun, t)
		}
	}
	mu.RUnlock()
	for _, t := range toRun {
		go runTaskOnce(t)
	}
	jsonResponse(w)
	json.NewEncoder(w).Encode(map[string]int{"triggered": len(toRun)})
}

func bulkToggleHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	var req bulkReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	mu.Lock()
	var toggled []*Task
	for _, id := range req.IDs {
		if t, ok := tasks[id]; ok {
			toggleTask(t)
			toggled = append(toggled, t)
		}
	}
	// Copy task data while under lock to avoid race
	tds := make([]*storage.TaskData, len(toggled))
	for i, t := range toggled {
		tds[i] = taskToData(t)
	}
	mu.Unlock()
	// Persist outside lock
	for _, td := range tds {
		if err := store.SaveTask(td); err != nil {
			log.Printf("[cron] Error persisting task %s: %v", td.ID, err)
		}
	}
	w.WriteHeader(204)
}

func bulkDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		w.WriteHeader(405)
		return
	}
	var req bulkReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	mu.Lock()
	var deletedIDs []string
	for _, id := range req.IDs {
		if t, ok := tasks[id]; ok {
			clearSchedule(t)
			delete(tasks, id)
			deletedIDs = append(deletedIDs, id)
		}
	}
	mu.Unlock()
	// Persist deletes sequentially outside lock (L4)
	for _, id := range deletedIDs {
		persistDelete(id)
	}
	w.WriteHeader(204)
}

// --- Phase 8: Import/Export ---

func exportHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	mu.RLock()
	out := make([]*Task, 0, len(tasks))
	for _, t := range tasks {
		cp := sanitizeTask(t)
		// Strip notification configs entirely — they contain credentials
		// that would be masked and broken on re-import (M7)
		cp.Notifications = nil
		out = append(out, cp)
	}
	mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=cron_export.json")
	json.NewEncoder(w).Encode(out)
}

func importHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	var imported []createReq
	if err := json.NewDecoder(r.Body).Decode(&imported); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), 400)
		return
	}
	mu.RLock()
	remaining := maxTasks - len(tasks)
	mu.RUnlock()
	if len(imported) > remaining {
		imported = imported[:remaining]
	}
	created := 0
	for _, req := range imported {
		if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Command) == "" {
			continue
		}
		if req.Type != "interval" && req.Type != "cron" {
			continue
		}
		id := newTaskID()
		t := &Task{
			ID: id, Name: req.Name, Command: req.Command, Type: req.Type,
			Status: "paused", CronExpr: req.CronExpr,
			TimeoutSec: req.TimeoutSec, RetryCount: req.RetryCount,
			RetryDelaySec: req.RetryDelaySec, Env: req.Env,
			Notifications: req.Notifications,
			Category: req.Category, Tags: req.Tags, Priority: req.Priority,
			DependsOn: req.DependsOn, AllowParallel: req.AllowParallel,
			MaxLogEntries: req.MaxLogEntries,
		}
		if req.Type == "interval" {
			if req.IntervalMin < 1 {
				continue
			}
			t.Interval = time.Duration(req.IntervalMin) * time.Minute
		} else {
			if !isValidCron(req.CronExpr) {
				continue
			}
		}
		mu.Lock()
		tasks[id] = t
		mu.Unlock()
		persistTask(t)
		created++
	}
	jsonResponse(w)
	json.NewEncoder(w).Encode(map[string]int{"imported": created})
}

// --- Phase 8: Health ---

func healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	mu.RLock()
	total := len(tasks)
	running := 0
	paused := 0
	var lastExec int64
	for _, t := range tasks {
		if t.Status == "running" {
			running++
		} else {
			paused++
		}
		if t.LastRunAt > lastExec {
			lastExec = t.LastRunAt
		}
	}
	mu.RUnlock()
	jsonResponse(w)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":         "healthy",
		"version":        version,
		"uptime_seconds": int64(time.Since(startTime).Seconds()),
		"tasks_total":    total,
		"tasks_running":  running,
		"tasks_paused":   paused,
		"last_execution": lastExec,
	})
}

// --- Task Templates ---

type taskTemplate struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Command     string `json:"command"`
	Type        string `json:"type"`
	IntervalMin int    `json:"interval_min,omitempty"`
	CronExpr    string `json:"cron_expr,omitempty"`
	Category    string `json:"category"`
	TimeoutSec  int    `json:"timeout_sec"`
}

var builtinTemplates = []taskTemplate{
	{
		ID: "backup-appdata", Name: "AppData Backup",
		Description: "Archive /DATA/AppData to /DATA/backups/",
		Command:     "mkdir -p /DATA/backups && tar -czf /DATA/backups/appdata_$(date +%Y%m%d_%H%M%S).tar.gz -C /DATA AppData",
		Type: "cron", CronExpr: "0 2 * * *",
		Category: "backup", TimeoutSec: 600,
	},
	{
		ID: "cleanup-tmp", Name: "Cleanup Temp Files",
		Description: "Remove files older than 7 days from /tmp",
		Command:     "find /tmp -type f -mtime +7 -delete 2>/dev/null; echo cleaned",
		Type: "cron", CronExpr: "0 4 * * 0",
		Category: "maintenance", TimeoutSec: 120,
	},
	{
		ID: "health-check", Name: "System Health Check",
		Description: "Check disk space, memory, and load average",
		Command:     "echo '=== Disk ===' && df -h / /DATA 2>/dev/null && echo '=== Memory ===' && free -h && echo '=== Load ===' && uptime",
		Type: "interval", IntervalMin: 30,
		Category: "monitoring", TimeoutSec: 30,
	},
	{
		ID: "docker-prune", Name: "Docker Cleanup",
		Description: "Remove unused Docker images, containers, and volumes",
		Command:     "DOCKER_CONFIG=/DATA/.docker docker system prune -af --volumes 2>&1 || echo 'docker not available'",
		Type: "cron", CronExpr: "0 3 * * 0",
		Category: "maintenance", TimeoutSec: 300,
	},
	{
		ID: "update-check", Name: "System Update Check",
		Description: "Check for available system updates",
		Command:     "cat /etc/os-release && echo '---' && uname -r",
		Type: "cron", CronExpr: "0 8 * * 1",
		Category: "monitoring", TimeoutSec: 60,
	},
	{
		ID: "ssl-cert-check", Name: "SSL Certificate Expiry Check",
		Description: "Check SSL certificate expiry for a domain",
		Command:     "echo | openssl s_client -connect example.com:443 -servername example.com 2>/dev/null | openssl x509 -noout -dates 2>/dev/null || echo 'openssl not available'",
		Type: "cron", CronExpr: "0 9 * * *",
		Category: "monitoring", TimeoutSec: 30,
	},
	{
		ID: "docker-status", Name: "Docker Container Status",
		Description: "List all Docker containers with status and resource usage",
		Command:     "docker ps -a --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}' 2>&1 && echo '---' && docker stats --no-stream --format 'table {{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}' 2>&1 || echo 'docker not available'",
		Type: "interval", IntervalMin: 15,
		Category: "monitoring", TimeoutSec: 30,
	},
}

func templatesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	jsonResponse(w)
	json.NewEncoder(w).Encode(builtinTemplates)
}

// --- Settings ---

func settingsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s, err := store.LoadSettings()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		// Mask the bot token for GET responses
		resp := struct {
			TelegramBotToken  string `json:"telegram_bot_token"`
			TelegramChatID    string `json:"telegram_chat_id"`
			TelegramOnSuccess bool   `json:"telegram_on_success"`
			TelegramOnFailure bool   `json:"telegram_on_failure"`
			TelegramConfigured bool  `json:"telegram_configured"`
		}{
			TelegramBotToken:  maskToken(s.TelegramBotToken),
			TelegramChatID:    s.TelegramChatID,
			TelegramOnSuccess: s.TelegramOnSuccess,
			TelegramOnFailure: s.TelegramOnFailure,
			TelegramConfigured: s.TelegramBotToken != "" && s.TelegramChatID != "",
		}
		jsonResponse(w)
		json.NewEncoder(w).Encode(resp)
	case http.MethodPut:
		var req struct {
			TelegramBotToken  string `json:"telegram_bot_token"`
			TelegramChatID    string `json:"telegram_chat_id"`
			TelegramOnSuccess bool   `json:"telegram_on_success"`
			TelegramOnFailure bool   `json:"telegram_on_failure"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		// If token is masked (unchanged), keep the existing one
		existing, _ := store.LoadSettings()
		token := req.TelegramBotToken
		if token == maskToken(existing.TelegramBotToken) || token == "" {
			token = existing.TelegramBotToken
		}
		s := &storage.Settings{
			TelegramBotToken:  token,
			TelegramChatID:    req.TelegramChatID,
			TelegramOnSuccess: req.TelegramOnSuccess,
			TelegramOnFailure: req.TelegramOnFailure,
		}
		if err := store.SaveSettings(s); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		jsonResponse(w)
		json.NewEncoder(w).Encode(map[string]string{"status": "saved"})
	default:
		w.WriteHeader(405)
	}
}

func maskToken(token string) string {
	if len(token) <= 8 {
		return strings.Repeat("*", len(token))
	}
	return token[:4] + strings.Repeat("*", len(token)-8) + token[len(token)-4:]
}

func testTelegramHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	var req struct {
		BotToken string `json:"bot_token"`
		ChatID   string `json:"chat_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// If token is masked, use stored one
	if strings.Contains(req.BotToken, "***") || req.BotToken == "" {
		existing, _ := store.LoadSettings()
		req.BotToken = existing.TelegramBotToken
	}
	if req.BotToken == "" || req.ChatID == "" {
		http.Error(w, "bot_token and chat_id required", 400)
		return
	}
	msg := "\u2705 <b>cron</b> test message!\nTelegram notifications are working."
	err := notify.SendTelegramMessage(req.BotToken, req.ChatID, msg)
	jsonResponse(w)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
	})
}

// getTelegramNotifyConfig returns a Telegram notification config from global settings.
// Returns nil if Telegram is not configured.
func getTelegramNotifyConfig() *notify.Config {
	if store == nil {
		return nil
	}
	s, err := store.LoadSettings()
	if err != nil || s.TelegramBotToken == "" || s.TelegramChatID == "" {
		return nil
	}
	return &notify.Config{
		Enabled:          true,
		Type:             "telegram",
		Target:           s.TelegramChatID,
		OnSuccess:        s.TelegramOnSuccess,
		OnFailure:        s.TelegramOnFailure,
		TelegramBotToken: s.TelegramBotToken,
	}
}

func scheduleCronNext(t *Task) {
	next := cronNext(t.CronExpr, time.Now())
	if next.IsZero() {
		return
	}
	delay := time.Until(next)
	if delay < 0 {
		delay = 0
	}
	t.NextRunAt = next.UnixMilli()
	t.timer = time.AfterFunc(delay, func() {
		if t.Status == "running" {
			runTaskOnce(t)
			scheduleCronNext(t)
		}
	})
}

func cronNext(expr string, from time.Time) time.Time {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return time.Time{}
	}
	minSet := parseCronField(f[0], 0, 59, false)
	hourSet := parseCronField(f[1], 0, 23, false)
	domSet := parseCronField(f[2], 1, 31, false)
	monSet := parseCronField(f[3], 1, 12, false)
	dowSet := parseCronField(f[4], 0, 6, true)
	d := from.Truncate(time.Minute).Add(time.Minute)
	deadline := d.Add(366 * 24 * time.Hour) // search up to 1 year ahead
	for d.Before(deadline) {
		m := d.Minute()
		h := d.Hour()
		dom := d.Day()
		mon := int(d.Month())
		dow := int(d.Weekday())
		if dow == 0 && dowSet.has7 {
			dow = 7
		}
		minuteOk := minSet.set[m]
		hourOk := hourSet.set[h]
		monthOk := monSet.set[mon]
		domOk := domSet.set[dom]
		dowOk := dowSet.set[dow]
		dayOk := (domSet.isAll && dowSet.isAll) || (domSet.isAll && dowOk) || (dowSet.isAll && domOk) || (domOk || dowOk)
		if minuteOk && hourOk && monthOk && dayOk {
			return d
		}
		d = d.Add(time.Minute)
	}
	return time.Time{}
}

type cronField struct {
	set   map[int]bool
	isAll bool
	has7  bool
}

func parseCronField(expr string, min, max int, isDow bool) cronField {
	cf := cronField{set: map[int]bool{}}
	tokens := strings.Split(strings.ToLower(strings.TrimSpace(expr)), ",")
	addRange := func(a, b, step int) {
		if step <= 0 {
			step = 1
		}
		for v := a; v <= b; v += step {
			cf.set[v] = true
		}
	}
	aliases := map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}
	for _, tok := range tokens {
		tok = strings.TrimSpace(tok)
		if tok == "*" {
			cf.isAll = true
			addRange(min, max, 1)
			continue
		}
		if strings.HasPrefix(tok, "*/") {
			step, err := strconv.Atoi(strings.TrimPrefix(tok, "*/"))
			if err != nil || step <= 0 {
				log.Printf("[cron] Invalid cron step %q, skipping", tok)
				continue
			}
			cf.isAll = true
			addRange(min, max, step)
			continue
		}
		if isDow {
			if v, ok := aliases[tok]; ok {
				cf.set[v] = true
				continue
			}
		}
		if strings.Contains(tok, "-") {
			parts := strings.Split(tok, "-")
			a, errA := strconv.Atoi(parts[0])
			bPart := parts[1]
			step := 1
			if strings.Contains(bPart, "/") {
				sub := strings.Split(bPart, "/")
				bPart = sub[0]
				s, errS := strconv.Atoi(sub[1])
				if errS != nil || s <= 0 {
					log.Printf("[cron] Invalid cron range step %q, skipping", tok)
					continue
				}
				step = s
			}
			b, errB := strconv.Atoi(bPart)
			if errA != nil || errB != nil {
				log.Printf("[cron] Invalid cron range %q, skipping", tok)
				continue
			}
			if isDow && b == 7 {
				cf.has7 = true
			}
			addRange(int(math.Max(float64(min), float64(a))), int(math.Min(float64(max), float64(b))), step)
			continue
		}
		if strings.Contains(tok, "/") {
			parts := strings.Split(tok, "/")
			if parts[0] == "*" {
				step, err := strconv.Atoi(parts[1])
				if err != nil || step <= 0 {
					log.Printf("[cron] Invalid cron step %q, skipping", tok)
					continue
				}
				addRange(min, max, step)
				continue
			}
		}
		v, err := strconv.Atoi(tok)
		if err != nil {
			log.Printf("[cron] Invalid cron token %q, skipping", tok)
			continue
		}
		if isDow && v == 7 {
			cf.has7 = true
		}
		if v >= min && v <= max {
			cf.set[v] = true
		}
	}
	return cf
}
