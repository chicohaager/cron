package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// TaskData is the persistable subset of a Task (no timers, no unexported fields).
type TaskData struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Command       string            `json:"command"`
	Type          string            `json:"type"`
	IntervalMs    int64             `json:"interval_ms"`
	CronExpr      string            `json:"cron_expr"`
	Status        string            `json:"status"`
	NextRunAt     int64             `json:"next_run_at"`
	LastRunAt     int64             `json:"last_run_at"`
	LastResult    *ResultData       `json:"last_result"`
	TimeoutSec    int               `json:"timeout_sec,omitempty"`
	RetryCount    int               `json:"retry_count,omitempty"`
	RetryDelaySec int               `json:"retry_delay_sec,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Notifications json.RawMessage   `json:"notifications,omitempty"`
	Category      string            `json:"category,omitempty"`
	Tags          []string          `json:"tags,omitempty"`
	Priority      int               `json:"priority,omitempty"`
	DependsOn     []string          `json:"depends_on,omitempty"`
	AllowParallel bool              `json:"allow_parallel,omitempty"`
	MaxLogEntries int               `json:"max_log_entries,omitempty"`
	// Logs is only read for migrating pre-0.3 files that stored the history
	// inline; new writes never populate it (see LoadLogs/SaveLogs).
	Logs []LogEntryData `json:"logs,omitempty"`
}

// LogEntryData is the persistable form of a log entry.
type LogEntryData struct {
	Time       int64  `json:"time"`
	DurationMs int64  `json:"duration_ms"`
	Success    bool   `json:"success"`
	Code       string `json:"code,omitempty"`
	Message    string `json:"message"`
}

// ResultData is the persistable form of a Result.
type ResultData struct {
	Success bool   `json:"success"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

// Settings holds global app configuration (e.g. notification credentials).
type Settings struct {
	TelegramBotToken  string `json:"telegram_bot_token,omitempty"`
	TelegramChatID    string `json:"telegram_chat_id,omitempty"`
	TelegramOnSuccess bool   `json:"telegram_on_success"`
	TelegramOnFailure bool   `json:"telegram_on_failure"`
}

// Storage defines the persistence interface for tasks, their run history
// and global settings.
type Storage interface {
	LoadTasks() ([]*TaskData, error)
	SaveTasks([]*TaskData) error
	SaveTask(*TaskData) error
	DeleteTask(id string) error
	LoadLogs(id string) ([]LogEntryData, error)
	SaveLogs(id string, logs []LogEntryData) error
	DeleteLogs(id string) error
	LoadSettings() (*Settings, error)
	SaveSettings(*Settings) error
}

// FileStorage implements Storage with JSON files and atomic writes.
//
// Layout under the base directory:
//
//	tasks.json      task definitions and last result (rewritten on change)
//	logs/<id>.json  run history of one task (rewritten after each run)
//	settings.json   global settings
//
// Keeping the history out of tasks.json matters at scale: with logs inline
// every run rewrote the whole file (266 KB for three tasks was measured on a
// production host); now a run touches only its own small file.
type FileStorage struct {
	path         string
	logsDir      string
	settingsPath string
	mu           sync.RWMutex
}

// NewFileStorage creates a FileStorage rooted at basePath, creating the
// directory tree if needed.
func NewFileStorage(basePath string) (*FileStorage, error) {
	logsDir := filepath.Join(basePath, "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		return nil, fmt.Errorf("create storage dir: %w", err)
	}
	return &FileStorage{
		path:         filepath.Join(basePath, "tasks.json"),
		logsDir:      logsDir,
		settingsPath: filepath.Join(basePath, "settings.json"),
	}, nil
}

func (fs *FileStorage) logPath(id string) string {
	return filepath.Join(fs.logsDir, filepath.Base(id)+".json")
}

// LoadLogs reads the run history of one task; a missing file is an empty history.
func (fs *FileStorage) LoadLogs(id string) ([]LogEntryData, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	data, err := os.ReadFile(fs.logPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return []LogEntryData{}, nil
		}
		return nil, fmt.Errorf("read logs %s: %w", id, err)
	}
	if len(data) == 0 {
		return []LogEntryData{}, nil
	}
	var logs []LogEntryData
	if err := json.Unmarshal(data, &logs); err != nil {
		return nil, fmt.Errorf("unmarshal logs %s: %w", id, err)
	}
	return logs, nil
}

// SaveLogs atomically replaces the run history of one task.
func (fs *FileStorage) SaveLogs(id string, logs []LogEntryData) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if logs == nil {
		logs = []LogEntryData{}
	}
	return writeJSONAtomic(fs.logPath(id), logs, 0644)
}

// DeleteLogs removes the run history of one task.
func (fs *FileStorage) DeleteLogs(id string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if err := os.Remove(fs.logPath(id)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete logs %s: %w", id, err)
	}
	return nil
}

// LoadTasks reads all tasks from the JSON file.
// Returns an empty slice (not error) if the file doesn't exist yet.
func (fs *FileStorage) LoadTasks() ([]*TaskData, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	data, err := os.ReadFile(fs.path)
	if err != nil {
		if os.IsNotExist(err) {
			return []*TaskData{}, nil
		}
		return nil, fmt.Errorf("read tasks file: %w", err)
	}
	if len(data) == 0 {
		return []*TaskData{}, nil
	}

	var tasks []*TaskData
	if err := json.Unmarshal(data, &tasks); err != nil {
		return nil, fmt.Errorf("unmarshal tasks: %w", err)
	}
	return tasks, nil
}

// SaveTasks atomically writes all tasks to the JSON file.
func (fs *FileStorage) SaveTasks(tasks []*TaskData) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.writeAtomic(tasks)
}

// SaveTask loads existing tasks, upserts the given task, and saves.
func (fs *FileStorage) SaveTask(task *TaskData) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	tasks, err := fs.readLocked()
	if err != nil {
		return err
	}

	found := false
	for i, t := range tasks {
		if t.ID == task.ID {
			tasks[i] = task
			found = true
			break
		}
	}
	if !found {
		tasks = append(tasks, task)
	}

	return fs.writeAtomic(tasks)
}

// DeleteTask removes a task by ID and saves.
func (fs *FileStorage) DeleteTask(id string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	tasks, err := fs.readLocked()
	if err != nil {
		return err
	}

	filtered := make([]*TaskData, 0, len(tasks))
	for _, t := range tasks {
		if t.ID != id {
			filtered = append(filtered, t)
		}
	}

	return fs.writeAtomic(filtered)
}

// readLocked reads without acquiring the lock (caller must hold it).
func (fs *FileStorage) readLocked() ([]*TaskData, error) {
	data, err := os.ReadFile(fs.path)
	if err != nil {
		if os.IsNotExist(err) {
			return []*TaskData{}, nil
		}
		return nil, fmt.Errorf("read tasks file: %w", err)
	}
	if len(data) == 0 {
		return []*TaskData{}, nil
	}
	var tasks []*TaskData
	if err := json.Unmarshal(data, &tasks); err != nil {
		return nil, fmt.Errorf("unmarshal tasks: %w", err)
	}
	return tasks, nil
}

// writeAtomic writes the task list to tasks.json. Caller holds fs.mu.
func (fs *FileStorage) writeAtomic(tasks []*TaskData) error {
	return writeJSONAtomic(fs.path, tasks, 0644)
}

// writeJSONAtomic marshals v and writes it via a temp file plus rename, so a
// crash mid-write never leaves a truncated file behind.
func writeJSONAtomic(path string, v interface{}, perm os.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", filepath.Base(path), err)
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, perm); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(tmpPath), err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath) // best-effort cleanup
		return fmt.Errorf("rename %s: %w", filepath.Base(path), err)
	}
	return nil
}

// LoadSettings reads settings from settings.json.
func (fs *FileStorage) LoadSettings() (*Settings, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	data, err := os.ReadFile(fs.settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &Settings{TelegramOnFailure: true}, nil
		}
		return nil, fmt.Errorf("read settings: %w", err)
	}
	if len(data) == 0 {
		return &Settings{TelegramOnFailure: true}, nil
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("unmarshal settings: %w", err)
	}
	return &s, nil
}

// SaveSettings atomically writes settings to settings.json.
func (fs *FileStorage) SaveSettings(s *Settings) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	return writeJSONAtomic(fs.settingsPath, s, 0600)
}
