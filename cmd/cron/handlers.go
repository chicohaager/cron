package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/chicohaager/cron/internal/storage"
	"github.com/chicohaager/lintux-modkit/httpx"
	"github.com/chicohaager/lintux-modkit/notify"
	"github.com/chicohaager/lintux-modkit/schedule"
)

// routePrefix is the path the gateway forwards to us; it is kept on the
// wire (the gateway does not strip it) and therefore part of every route.
const routePrefix = "/cron"

// newMux wires every API route. exempt paths are served without a session
// token; everything else goes through the verifier when one is configured.
func newMux(verify func(http.Handler) http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	logged := httpx.Logging("cron", maxRequestBody)
	open := func(pattern string, h http.HandlerFunc) {
		mux.Handle(routePrefix+pattern, logged(h))
	}
	guarded := func(pattern string, h http.HandlerFunc) {
		mux.Handle(routePrefix+pattern, logged(verify(h)))
	}
	open("/health", healthHandler)
	guarded("/tasks", tasksHandler)
	guarded("/tasks/", taskActionHandler)
	guarded("/tasks/bulk/run", bulkHandler(bulkRun))
	guarded("/tasks/bulk/toggle", bulkHandler(bulkToggle))
	guarded("/tasks/bulk/delete", bulkHandler(bulkDelete))
	guarded("/categories", categoriesHandler)
	guarded("/tags", tagsHandler)
	guarded("/cron/validate", cronValidateHandler)
	guarded("/export", exportHandler)
	guarded("/import", importHandler)
	guarded("/templates", templatesHandler)
	guarded("/settings", settingsHandler)
	guarded("/settings/test-telegram", testTelegramHandler)
	return mux
}

// staticDir is where the sysext ships the UI; CRON_STATIC_DIR overrides it
// for development.
const staticDir = "/usr/share/casaos/www/modules/cron"

func withStatic(next http.Handler) http.Handler {
	dir := staticDir
	if env := os.Getenv("CRON_STATIC_DIR"); env != "" {
		dir = env
	}
	return httpx.Static("/modules/cron/", dir, next)
}

// --- response helpers (thin names over httpx so handlers stay short) ---

var (
	writeJSON        = httpx.WriteJSON
	writeError       = httpx.WriteError
	methodNotAllowed = httpx.MethodNotAllowed
	notFound         = httpx.NotFound
	decodeBody       = httpx.Decode
)

func writeRequestError(w http.ResponseWriter, err error) { httpx.WriteErr(w, err) }

// --- tasks ---

func tasksHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		category := r.URL.Query().Get("category")
		tag := r.URL.Query().Get("tag")
		mu.RLock()
		out := make([]taskView, 0, len(tasks))
		for _, t := range sortedTasksLocked() {
			if (category != "" && t.Category != category) || (tag != "" && !hasTag(t.Tags, tag)) {
				continue
			}
			out = append(out, viewLocked(t))
		}
		mu.RUnlock()
		writeJSON(w, http.StatusOK, out)
	case http.MethodPost:
		var req taskRequest
		if !decodeBody(w, r, &req) {
			return
		}
		t, err := createTask(req)
		if err != nil {
			writeRequestError(w, err)
			return
		}
		mu.RLock()
		v := viewLocked(t)
		mu.RUnlock()
		writeJSON(w, http.StatusCreated, v)
	default:
		methodNotAllowed(w)
	}
}

// createTask validates, registers, schedules and persists a new task.
func createTask(req taskRequest) (*Task, error) {
	mu.Lock()
	if len(tasks) >= maxTasks {
		mu.Unlock()
		return nil, reqErr("task_limit", "task limit reached (%d)", maxTasks)
	}
	if err := validateRequestLocked(&req, ""); err != nil {
		mu.Unlock()
		return nil, err
	}
	t := &Task{ID: newTaskID(), Status: statusRunning}
	applyRequestLocked(t, req)
	t.Notifications = req.Notifications
	tasks[t.ID] = t
	startSchedule(t)
	mu.Unlock()
	persistTask(t)
	return t, nil
}

func taskActionHandler(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, routePrefix+"/tasks/"), "/")
	id := parts[0]
	mu.RLock()
	t := tasks[id]
	mu.RUnlock()
	if t == nil {
		notFound(w)
		return
	}
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			mu.RLock()
			v := viewLocked(t)
			mu.RUnlock()
			writeJSON(w, http.StatusOK, v)
		case http.MethodPut:
			updateTaskHandler(w, r, t)
		case http.MethodDelete:
			mu.Lock()
			clearSchedule(t)
			delete(tasks, id)
			mu.Unlock()
			persistDelete(id)
			w.WriteHeader(http.StatusNoContent)
		default:
			methodNotAllowed(w)
		}
		return
	}
	switch parts[1] {
	case "run":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		go runTaskOnce(t)
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "triggered"})
	case "toggle":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		mu.Lock()
		toggleTask(t)
		v := viewLocked(t)
		mu.Unlock()
		persistTask(t)
		writeJSON(w, http.StatusOK, v)
	case "logs":
		logsHandler(w, r, t, parts[2:])
	default:
		notFound(w)
	}
}

func updateTaskHandler(w http.ResponseWriter, r *http.Request, t *Task) {
	var req taskRequest
	if !decodeBody(w, r, &req) {
		return
	}
	mu.Lock()
	if err := validateRequestLocked(&req, t.ID); err != nil {
		mu.Unlock()
		writeRequestError(w, err)
		return
	}
	reschedule := scheduleChanged(t, req)
	applyRequestLocked(t, req)
	t.Notifications = mergeCredentials(req.Notifications, t.Notifications)
	if reschedule && t.Status == statusRunning {
		startSchedule(t)
	}
	v := viewLocked(t)
	mu.Unlock()
	persistTask(t)
	writeJSON(w, http.StatusOK, v)
}

func logsHandler(w http.ResponseWriter, r *http.Request, t *Task, rest []string) {
	if r.Method == http.MethodPost && len(rest) == 1 && rest[0] == "clear" {
		mu.Lock()
		t.logs = nil
		mu.Unlock()
		persistLogs(t)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet || len(rest) != 0 {
		methodNotAllowed(w)
		return
	}
	mu.RLock()
	logs := append([]LogEntry(nil), t.logs...)
	mu.RUnlock()
	logs = filterLogs(logs, r.URL.Query())
	if r.URL.Query().Get("format") == "csv" {
		writeLogsCSV(w, t.ID, logs)
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

// filterLogs applies ?from=, ?to= (unix ms) and ?search= (case-insensitive
// substring on the message).
func filterLogs(logs []LogEntry, q map[string][]string) []LogEntry {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	from, _ := strconv.ParseInt(get("from"), 10, 64)
	to, _ := strconv.ParseInt(get("to"), 10, 64)
	search := strings.ToLower(get("search"))
	out := logs[:0]
	for _, l := range logs {
		if from != 0 && l.Time < from {
			continue
		}
		if to != 0 && l.Time > to {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(l.Message), search) {
			continue
		}
		out = append(out, l)
	}
	if out == nil {
		out = []LogEntry{}
	}
	return out
}

// writeLogsCSV streams the history as CSV. Cells are flattened to one line
// and formula-leading characters are quoted so a spreadsheet does not
// execute a command's output.
func writeLogsCSV(w http.ResponseWriter, id string, logs []LogEntry) {
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s_logs.csv", id))
	fmt.Fprintln(w, "time,duration_ms,success,code,message")
	for _, l := range logs {
		fmt.Fprintf(w, "%d,%d,%t,%s,\"%s\"\n", l.Time, l.DurationMs, l.Success, l.Code, csvCell(l.Message))
	}
}

func csvCell(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ", "\"", "\"\"").Replace(s)
	if len(s) > 0 && strings.ContainsRune("=+-@|", rune(s[0])) {
		s = "'" + s
	}
	return s
}

// --- bulk ---

type bulkOp func(t *Task)

func bulkRun(t *Task) { go runTaskOnce(t) }

func bulkToggle(t *Task) {
	mu.Lock()
	toggleTask(t)
	mu.Unlock()
	persistTask(t)
}

func bulkDelete(t *Task) {
	mu.Lock()
	clearSchedule(t)
	delete(tasks, t.ID)
	mu.Unlock()
	persistDelete(t.ID)
}

func bulkHandler(op bulkOp) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var req struct {
			IDs []string `json:"ids"`
		}
		if !decodeBody(w, r, &req) {
			return
		}
		mu.RLock()
		var selected []*Task
		for _, id := range req.IDs {
			if t, ok := tasks[id]; ok {
				selected = append(selected, t)
			}
		}
		mu.RUnlock()
		for _, t := range selected {
			op(t)
		}
		writeJSON(w, http.StatusOK, map[string]int{"affected": len(selected)})
	}
}

// --- metadata ---

func categoriesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	mu.RLock()
	cats := uniqueSorted(func(t *Task) []string { return []string{t.Category} })
	mu.RUnlock()
	writeJSON(w, http.StatusOK, cats)
}

func tagsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	mu.RLock()
	tags := uniqueSorted(func(t *Task) []string { return t.Tags })
	mu.RUnlock()
	writeJSON(w, http.StatusOK, tags)
}

// uniqueSorted collects non-empty values from every task in name order. Caller holds mu.
func uniqueSorted(pick func(*Task) []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, t := range sortedTasksLocked() {
		for _, v := range pick(t) {
			if v != "" && !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
}

func cronValidateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Expr string `json:"expr"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	errs, valid := schedule.Validate(req.Expr)
	resp := struct {
		Valid    bool                       `json:"valid"`
		Errors   []schedule.ValidationError `json:"errors"`
		NextRuns []int64                    `json:"next_runs"`
	}{Valid: valid, Errors: errs, NextRuns: []int64{}}
	if resp.Errors == nil {
		resp.Errors = []schedule.ValidationError{}
	}
	now := time.Now()
	for i := 0; valid && i < 5; i++ {
		next := schedule.Next(req.Expr, now)
		if next.IsZero() {
			break
		}
		resp.NextRuns = append(resp.NextRuns, next.UnixMilli())
		now = next
	}
	writeJSON(w, http.StatusOK, resp)
}

// --- import / export ---

const exportVersion = 1

type exportFile struct {
	Version    int           `json:"version"`
	ExportedAt int64         `json:"exported_at"`
	Tasks      []taskRequest `json:"tasks"`
}

func exportHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	mu.RLock()
	out := exportFile{Version: exportVersion, ExportedAt: time.Now().UnixMilli()}
	for _, t := range sortedTasksLocked() {
		out.Tasks = append(out.Tasks, exportOf(t))
	}
	mu.RUnlock()
	if out.Tasks == nil {
		out.Tasks = []taskRequest{}
	}
	w.Header().Set("Content-Disposition", "attachment; filename=cron_export.json")
	writeJSON(w, http.StatusOK, out)
}

// importHandler accepts the export envelope or a bare task array. Imported
// tasks start paused and are created one by one; anything rejected is named
// in the response instead of being dropped silently.
func importHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var raw json.RawMessage
	if !decodeBody(w, r, &raw) {
		return
	}
	var incoming []taskRequest
	var envelope exportFile
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Tasks != nil {
		incoming = envelope.Tasks
	} else if err := json.Unmarshal(raw, &incoming); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", "expected an export file or a task array")
		return
	}
	type skipped struct {
		Name   string `json:"name"`
		Reason string `json:"reason"`
		Code   string `json:"code"`
	}
	resp := struct {
		Imported int       `json:"imported"`
		Skipped  []skipped `json:"skipped"`
	}{Skipped: []skipped{}}
	for _, req := range incoming {
		req.DependsOn = nil // IDs from another host are meaningless here
		mu.Lock()
		if len(tasks) >= maxTasks {
			mu.Unlock()
			resp.Skipped = append(resp.Skipped, skipped{req.Name, "task limit reached", "task_limit"})
			continue
		}
		if err := validateRequestLocked(&req, ""); err != nil {
			mu.Unlock()
			var re *httpx.Error
			code := "invalid"
			if errors.As(err, &re) {
				code = re.Code
			}
			resp.Skipped = append(resp.Skipped, skipped{req.Name, err.Error(), code})
			continue
		}
		t := &Task{ID: newTaskID(), Status: statusPaused}
		applyRequestLocked(t, req)
		t.Notifications = req.Notifications
		tasks[t.ID] = t
		mu.Unlock()
		persistTask(t)
		resp.Imported++
	}
	writeJSON(w, http.StatusOK, resp)
}

// --- health ---

func healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	mu.RLock()
	total, running := len(tasks), 0
	var lastExec int64
	for _, t := range tasks {
		if t.Status == statusRunning {
			running++
		}
		if t.LastRunAt > lastExec {
			lastExec = t.LastRunAt
		}
	}
	mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":         "healthy",
		"version":        version,
		"uptime_seconds": int64(time.Since(startTime).Seconds()),
		"tasks_total":    total,
		"tasks_running":  running,
		"tasks_paused":   total - running,
		"last_execution": lastExec,
	})
}

func templatesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, builtinTemplates)
}

// --- settings ---

type settingsView struct {
	TelegramBotToken   string `json:"telegram_bot_token"`
	TelegramChatID     string `json:"telegram_chat_id"`
	TelegramOnSuccess  bool   `json:"telegram_on_success"`
	TelegramOnFailure  bool   `json:"telegram_on_failure"`
	TelegramConfigured bool   `json:"telegram_configured"`
}

func settingsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s, err := store.LoadSettings()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, settingsView{
			TelegramBotToken:   maskToken(s.TelegramBotToken),
			TelegramChatID:     s.TelegramChatID,
			TelegramOnSuccess:  s.TelegramOnSuccess,
			TelegramOnFailure:  s.TelegramOnFailure,
			TelegramConfigured: s.TelegramBotToken != "" && s.TelegramChatID != "",
		})
	case http.MethodPut:
		var req settingsView
		if !decodeBody(w, r, &req) {
			return
		}
		existing, err := store.LoadSettings()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage", err.Error())
			return
		}
		token := req.TelegramBotToken
		if token == "" || token == maskToken(existing.TelegramBotToken) {
			token = existing.TelegramBotToken
		}
		s := &storage.Settings{
			TelegramBotToken:  token,
			TelegramChatID:    strings.TrimSpace(req.TelegramChatID),
			TelegramOnSuccess: req.TelegramOnSuccess,
			TelegramOnFailure: req.TelegramOnFailure,
		}
		if err := store.SaveSettings(s); err != nil {
			writeError(w, http.StatusInternalServerError, "storage", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
	default:
		methodNotAllowed(w)
	}
}

// maskToken keeps the first and last four characters so the user can
// recognise which token is stored without the API ever returning it whole.
func maskToken(token string) string {
	if len(token) <= 8 {
		return strings.Repeat("*", len(token))
	}
	return token[:4] + strings.Repeat("*", len(token)-8) + token[len(token)-4:]
}

func testTelegramHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		BotToken string `json:"bot_token"`
		ChatID   string `json:"chat_id"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if req.BotToken == "" || strings.Contains(req.BotToken, "***") {
		if existing, err := store.LoadSettings(); err == nil {
			req.BotToken = existing.TelegramBotToken
		}
	}
	if req.BotToken == "" || req.ChatID == "" {
		writeError(w, http.StatusBadRequest, "telegram_incomplete", "bot_token and chat_id required")
		return
	}
	msg := "✅ <b>cron</b> test message\nTelegram notifications are working."
	if err := notify.SendTelegramMessage(req.BotToken, req.ChatID, msg); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// getTelegramNotifyConfig returns the global Telegram channel, or nil when
// it is not configured.
func getTelegramNotifyConfig() *notify.Config {
	if store == nil {
		return nil
	}
	s, err := store.LoadSettings()
	if err != nil || s.TelegramBotToken == "" || s.TelegramChatID == "" {
		return nil
	}
	return &notify.Config{
		Enabled: true, Type: "telegram", Target: s.TelegramChatID,
		OnSuccess: s.TelegramOnSuccess, OnFailure: s.TelegramOnFailure,
		TelegramBotToken: s.TelegramBotToken,
	}
}
