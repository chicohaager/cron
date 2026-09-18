package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	cronpkg "github.com/chicohaager/cron/internal/cron"
	"github.com/chicohaager/cron/internal/notify"
	"github.com/chicohaager/cron/internal/storage"
)

const (
	statusRunning = "running"
	statusPaused  = "paused"

	typeInterval = "interval"
	typeCron     = "cron"

	maskedSecret = "********"
)

// Task is the in-memory representation of a scheduled job. It is never
// serialised directly: the API speaks taskView, the disk speaks
// storage.TaskData. Timer state at the bottom is owned by scheduler.go.
type Task struct {
	ID       string
	Name     string
	Command  string
	Type     string
	Interval time.Duration
	CronExpr string
	Status   string

	NextRunAt  int64
	LastRunAt  int64
	LastResult *Result

	TimeoutSec    int
	RetryCount    int
	RetryDelaySec int
	CurrentRetry  int
	Env           map[string]string
	Notifications []notify.Config

	Category      string
	Tags          []string
	Priority      int
	DependsOn     []string
	AllowParallel bool
	MaxLogEntries int

	Executing bool

	logs       []LogEntry
	timer      *time.Timer
	ticker     *time.Ticker
	done       chan struct{}
	retryTimer *time.Timer
	gen        uint64 // bumped on every (re)arm/clear; callbacks compare it
}

// Result is the outcome of the last run. Code is a stable identifier the UI
// translates (see scheduler.go); Message is raw command output.
type Result struct {
	Success bool   `json:"success"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// LogEntry is one line of a task's run history.
type LogEntry struct {
	Time       int64  `json:"time"`
	DurationMs int64  `json:"duration_ms"`
	Success    bool   `json:"success"`
	Code       string `json:"code"`
	Message    string `json:"message"`
}

// taskView is the JSON shape of a task in API responses. Interval is
// exposed in minutes (the unit the API accepts) and in real milliseconds;
// the pre-0.3 API leaked Go's nanosecond Duration under "interval_ms".
type taskView struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Command       string            `json:"command"`
	Type          string            `json:"type"`
	IntervalMin   int               `json:"interval_min,omitempty"`
	IntervalMs    int64             `json:"interval_ms,omitempty"`
	CronExpr      string            `json:"cron_expr,omitempty"`
	Status        string            `json:"status"`
	NextRunAt     int64             `json:"next_run_at"`
	LastRunAt     int64             `json:"last_run_at"`
	LastResult    *Result           `json:"last_result"`
	TimeoutSec    int               `json:"timeout_sec"`
	RetryCount    int               `json:"retry_count"`
	RetryDelaySec int               `json:"retry_delay_sec"`
	CurrentRetry  int               `json:"current_retry"`
	Env           map[string]string `json:"env,omitempty"`
	Notifications []notify.Config   `json:"notifications,omitempty"`
	Category      string            `json:"category,omitempty"`
	Tags          []string          `json:"tags,omitempty"`
	Priority      int               `json:"priority,omitempty"`
	DependsOn     []string          `json:"depends_on,omitempty"`
	AllowParallel bool              `json:"allow_parallel"`
	MaxLogEntries int               `json:"max_log_entries,omitempty"`
	Executing     bool              `json:"executing"`
}

// viewLocked renders the task for the API with credentials masked. Caller holds mu.
func viewLocked(t *Task) taskView {
	v := taskView{
		ID: t.ID, Name: t.Name, Command: t.Command, Type: t.Type,
		CronExpr: t.CronExpr, Status: t.Status,
		NextRunAt: t.NextRunAt, LastRunAt: t.LastRunAt,
		TimeoutSec: t.TimeoutSec, RetryCount: t.RetryCount, RetryDelaySec: t.RetryDelaySec,
		CurrentRetry: t.CurrentRetry, Env: t.Env,
		Notifications: maskNotifications(t.Notifications),
		Category:      t.Category, Tags: t.Tags, Priority: t.Priority,
		DependsOn: t.DependsOn, AllowParallel: t.AllowParallel,
		MaxLogEntries: t.MaxLogEntries, Executing: t.Executing,
	}
	if t.LastResult != nil {
		r := *t.LastResult
		v.LastResult = &r
	}
	if t.Type == typeInterval {
		v.IntervalMin = int(t.Interval / time.Minute)
		v.IntervalMs = t.Interval.Milliseconds()
	}
	return v
}

// maskNotifications returns a copy with every credential replaced by
// maskedSecret; the PUT handler recognises the placeholder and keeps the
// stored value.
func maskNotifications(in []notify.Config) []notify.Config {
	if len(in) == 0 {
		return nil
	}
	out := make([]notify.Config, len(in))
	copy(out, in)
	for i := range out {
		if out[i].SMTPPass != "" {
			out[i].SMTPPass = maskedSecret
		}
		if out[i].TelegramBotToken != "" {
			out[i].TelegramBotToken = maskedSecret
		}
	}
	return out
}

// taskRequest is the body of POST /tasks, PUT /tasks/{id} and each entry of
// an import. It is also the export format, so a file written by GET /export
// round-trips through POST /import without loss.
type taskRequest struct {
	Name          string            `json:"name"`
	Command       string            `json:"command"`
	Type          string            `json:"type"`
	IntervalMin   int               `json:"interval_min,omitempty"`
	CronExpr      string            `json:"cron_expr,omitempty"`
	TimeoutSec    int               `json:"timeout_sec,omitempty"`
	RetryCount    int               `json:"retry_count,omitempty"`
	RetryDelaySec int               `json:"retry_delay_sec,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Notifications []notify.Config   `json:"notifications,omitempty"`
	Category      string            `json:"category,omitempty"`
	Tags          []string          `json:"tags,omitempty"`
	Priority      int               `json:"priority,omitempty"`
	DependsOn     []string          `json:"depends_on,omitempty"`
	AllowParallel bool              `json:"allow_parallel,omitempty"`
	MaxLogEntries int               `json:"max_log_entries,omitempty"`
}

// requestError is a validation failure with a stable code for the UI.
type requestError struct {
	code string
	msg  string
}

func (e *requestError) Error() string { return e.msg }

func reqErr(code, format string, a ...interface{}) error {
	return &requestError{code: code, msg: fmt.Sprintf(format, a...)}
}

// validateRequest checks everything that does not depend on other tasks.
// selfID is the task being edited (empty on create) so a task cannot depend
// on itself. Caller holds mu (dependency lookups read the registry).
func validateRequestLocked(req *taskRequest, selfID string) error {
	req.Name = strings.TrimSpace(req.Name)
	req.Command = strings.TrimSpace(req.Command)
	req.CronExpr = strings.TrimSpace(req.CronExpr)
	if req.Name == "" {
		return reqErr("name_required", "name is required")
	}
	if req.Command == "" {
		return reqErr("command_required", "command is required")
	}
	switch req.Type {
	case typeInterval:
		if req.IntervalMin < 1 {
			return reqErr("interval_invalid", "interval_min must be >= 1")
		}
	case typeCron:
		if errs, ok := cronpkg.Validate(req.CronExpr); !ok {
			return reqErr("cron_invalid", "invalid cron expression: %s", errs[0].Message)
		}
		if cronpkg.Next(req.CronExpr, time.Now()).IsZero() {
			return reqErr("cron_never_fires", "cron expression never fires within a year")
		}
	default:
		return reqErr("type_invalid", "type must be interval or cron")
	}
	if req.Priority < 0 || req.Priority > 10 {
		return reqErr("priority_invalid", "priority must be between 1 and 10")
	}
	if req.TimeoutSec < 0 || req.RetryCount < 0 || req.RetryDelaySec < 0 || req.MaxLogEntries < 0 {
		return reqErr("value_negative", "timeout, retry and log settings must not be negative")
	}
	if err := validateNotifications(req.Notifications); err != nil {
		return err
	}
	for _, id := range req.DependsOn {
		if id == selfID {
			return reqErr("dependency_self", "a task cannot depend on itself")
		}
		if _, ok := tasks[id]; !ok {
			return reqErr("dependency_unknown", "unknown dependency %q", id)
		}
	}
	if selfID != "" && dependencyCycleLocked(selfID, req.DependsOn) {
		return reqErr("dependency_cycle", "dependencies form a cycle")
	}
	return nil
}

func validateNotifications(notifs []notify.Config) error {
	for _, n := range notifs {
		switch n.Type {
		case "webhook":
			if err := notify.ValidateWebhookFormat(n.WebhookFormat); err != nil {
				return reqErr("notification_invalid", "%s", err.Error())
			}
			if err := notify.ValidateWebhookURL(n.Target); err != nil {
				return reqErr("notification_invalid", "%s", err.Error())
			}
		case "email", "telegram":
		default:
			return reqErr("notification_invalid", "invalid notification type %q", n.Type)
		}
	}
	return nil
}

// dependencyCycleLocked reports whether making selfID depend on deps would
// create a cycle, by walking the existing graph from each dependency. Caller holds mu.
func dependencyCycleLocked(selfID string, deps []string) bool {
	seen := map[string]bool{}
	var reaches func(id string) bool
	reaches = func(id string) bool {
		if id == selfID {
			return true
		}
		if seen[id] {
			return false
		}
		seen[id] = true
		if t, ok := tasks[id]; ok {
			for _, next := range t.DependsOn {
				if reaches(next) {
					return true
				}
			}
		}
		return false
	}
	for _, d := range deps {
		if reaches(d) {
			return true
		}
	}
	return false
}

// applyRequestLocked copies a validated request onto the task. Caller holds mu.
func applyRequestLocked(t *Task, req taskRequest) {
	t.Name, t.Command, t.Type = req.Name, req.Command, req.Type
	t.TimeoutSec, t.RetryCount, t.RetryDelaySec = req.TimeoutSec, req.RetryCount, req.RetryDelaySec
	t.Env, t.Category, t.Tags, t.Priority = req.Env, req.Category, req.Tags, req.Priority
	t.DependsOn, t.AllowParallel, t.MaxLogEntries = req.DependsOn, req.AllowParallel, req.MaxLogEntries
	if req.Type == typeInterval {
		t.Interval, t.CronExpr = time.Duration(req.IntervalMin)*time.Minute, ""
	} else {
		t.Interval, t.CronExpr = 0, req.CronExpr
	}
}

// scheduleChanged reports whether req alters when the task fires.
func scheduleChanged(t *Task, req taskRequest) bool {
	if t.Type != req.Type {
		return true
	}
	if req.Type == typeInterval {
		return int(t.Interval/time.Minute) != req.IntervalMin
	}
	return t.CronExpr != req.CronExpr
}

// mergeCredentials keeps stored secrets where the request carries the mask
// placeholder (the UI echoes masked values back on edit).
func mergeCredentials(incoming, existing []notify.Config) []notify.Config {
	for i := range incoming {
		if incoming[i].SMTPPass == maskedSecret {
			incoming[i].SMTPPass = findExisting(existing, "email", incoming[i].Target).SMTPPass
		}
		if incoming[i].TelegramBotToken == maskedSecret {
			incoming[i].TelegramBotToken = findExisting(existing, "telegram", incoming[i].Target).TelegramBotToken
		}
	}
	return incoming
}

func findExisting(existing []notify.Config, typ, target string) notify.Config {
	for _, e := range existing {
		if e.Type == typ && e.Target == target {
			return e
		}
	}
	return notify.Config{}
}

// exportOf renders a task in the import/export format. Notification configs
// are stripped: they carry credentials that would be masked on the way out
// and useless on the way back in.
func exportOf(t *Task) taskRequest {
	r := taskRequest{
		Name: t.Name, Command: t.Command, Type: t.Type, CronExpr: t.CronExpr,
		TimeoutSec: t.TimeoutSec, RetryCount: t.RetryCount, RetryDelaySec: t.RetryDelaySec,
		Env: t.Env, Category: t.Category, Tags: t.Tags, Priority: t.Priority,
		AllowParallel: t.AllowParallel, MaxLogEntries: t.MaxLogEntries,
	}
	if t.Type == typeInterval {
		r.IntervalMin = int(t.Interval / time.Minute)
	}
	return r
}

func newTaskID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return hex.EncodeToString(b)
}

// sortedTasksLocked returns the registry as a slice ordered by priority
// (10 first) and then by name, which is the order the UI shows. Caller holds mu.
func sortedTasksLocked() []*Task {
	out := make([]*Task, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

func hasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

// --- persistence mapping ---

func taskToData(t *Task) *storage.TaskData {
	td := &storage.TaskData{
		ID: t.ID, Name: t.Name, Command: t.Command, Type: t.Type,
		IntervalMs: t.Interval.Milliseconds(), CronExpr: t.CronExpr, Status: t.Status,
		NextRunAt: t.NextRunAt, LastRunAt: t.LastRunAt,
		TimeoutSec: t.TimeoutSec, RetryCount: t.RetryCount, RetryDelaySec: t.RetryDelaySec,
		Env: t.Env, Category: t.Category, Tags: t.Tags, Priority: t.Priority,
		DependsOn: t.DependsOn, AllowParallel: t.AllowParallel, MaxLogEntries: t.MaxLogEntries,
	}
	if t.LastResult != nil {
		td.LastResult = &storage.ResultData{Success: t.LastResult.Success, Code: t.LastResult.Code, Message: t.LastResult.Message}
	}
	if len(t.Notifications) > 0 {
		if data, err := json.Marshal(t.Notifications); err == nil {
			td.Notifications = data
		}
	}
	return td
}

func dataToTask(td *storage.TaskData) *Task {
	t := &Task{
		ID: td.ID, Name: td.Name, Command: td.Command, Type: td.Type,
		Interval: time.Duration(td.IntervalMs) * time.Millisecond, CronExpr: td.CronExpr, Status: td.Status,
		NextRunAt: td.NextRunAt, LastRunAt: td.LastRunAt,
		TimeoutSec: td.TimeoutSec, RetryCount: td.RetryCount, RetryDelaySec: td.RetryDelaySec,
		Env: td.Env, Category: td.Category, Tags: td.Tags, Priority: td.Priority,
		DependsOn: td.DependsOn, AllowParallel: td.AllowParallel, MaxLogEntries: td.MaxLogEntries,
	}
	if td.LastResult != nil {
		t.LastResult = &Result{Success: td.LastResult.Success, Code: td.LastResult.Code, Message: td.LastResult.Message}
	}
	if len(td.Notifications) > 0 {
		var configs []notify.Config
		if err := json.Unmarshal(td.Notifications, &configs); err == nil {
			t.Notifications = configs
		}
	}
	return t
}

func logsToData(logs []LogEntry) []storage.LogEntryData {
	out := make([]storage.LogEntryData, len(logs))
	for i, l := range logs {
		out[i] = storage.LogEntryData{Time: l.Time, DurationMs: l.DurationMs, Success: l.Success, Code: l.Code, Message: l.Message}
	}
	return out
}

func logsFromData(data []storage.LogEntryData) []LogEntry {
	out := make([]LogEntry, len(data))
	for i, l := range data {
		out[i] = LogEntry{Time: l.Time, DurationMs: l.DurationMs, Success: l.Success, Code: l.Code, Message: l.Message}
	}
	return out
}

// loadPersistedTasks restores the registry from disk and re-arms every
// running task. Files written by 0.2.x carry the history inline; it is moved
// to logs/<id>.json on first load so the next save of tasks.json drops it.
func loadPersistedTasks() error {
	persisted, err := store.LoadTasks()
	if err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	running := 0
	for _, td := range persisted {
		t := dataToTask(td)
		if len(td.Logs) > 0 {
			t.logs = logsFromData(td.Logs)
			if err := store.SaveLogs(t.ID, td.Logs); err != nil {
				log.Printf("[cron] migrate logs of %s: %v", t.ID, err)
			}
		} else if data, err := store.LoadLogs(t.ID); err == nil {
			t.logs = logsFromData(data)
		} else {
			log.Printf("[cron] load logs of %s: %v", t.ID, err)
		}
		tasks[t.ID] = t
		if t.Status == statusRunning {
			startSchedule(t)
			running++
		}
	}
	log.Printf("[cron] Loaded %d tasks from storage (%d running)", len(persisted), running)
	return nil
}
