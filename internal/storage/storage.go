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
	Logs          []LogEntryData    `json:"logs,omitempty"`
}

// LogEntryData is the persistable form of a log entry.
type LogEntryData struct {
	Time       int64  `json:"time"`
	DurationMs int64  `json:"duration_ms"`
	Success    bool   `json:"success"`
	Message    string `json:"message"`
}

// ResultData is the persistable form of a Result.
type ResultData struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// Settings holds global app configuration (e.g. notification credentials).
type Settings struct {
	TelegramBotToken  string `json:"telegram_bot_token,omitempty"`
	TelegramChatID    string `json:"telegram_chat_id,omitempty"`
	TelegramOnSuccess bool   `json:"telegram_on_success"`
	TelegramOnFailure bool   `json:"telegram_on_failure"`
}

// Storage defines the persistence interface for tasks.
type Storage interface {
	LoadTasks() ([]*TaskData, error)
	SaveTasks([]*TaskData) error
	SaveTask(*TaskData) error
	DeleteTask(id string) error
	LoadSettings() (*Settings, error)
	SaveSettings(*Settings) error
}

// FileStorage implements Storage using a JSON file with atomic writes.
type FileStorage struct {
	path         string
	settingsPath string
	mu           sync.RWMutex
}

// NewFileStorage creates a FileStorage that persists to basePath/tasks.json.
// It creates the directory if it doesn't exist.
func NewFileStorage(basePath string) (*FileStorage, error) {
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("create storage dir: %w", err)
	}
	return &FileStorage{
		path:         filepath.Join(basePath, "tasks.json"),
		settingsPath: filepath.Join(basePath, "settings.json"),
	}, nil
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

// writeAtomic writes to a temp file then renames for crash safety.
func (fs *FileStorage) writeAtomic(tasks []*TaskData) error {
	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal tasks: %w", err)
	}

	tmpPath := fs.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := os.Rename(tmpPath, fs.path); err != nil {
		os.Remove(tmpPath) // best-effort cleanup
		return fmt.Errorf("rename temp to tasks file: %w", err)
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

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	tmpPath := fs.settingsPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("write settings temp: %w", err)
	}
	if err := os.Rename(tmpPath, fs.settingsPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename settings: %w", err)
	}
	return nil
}
