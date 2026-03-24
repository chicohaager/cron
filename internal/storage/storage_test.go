package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func tempStorage(t *testing.T) *FileStorage {
	t.Helper()
	dir := t.TempDir()
	fs, err := NewFileStorage(dir)
	if err != nil {
		t.Fatalf("NewFileStorage: %v", err)
	}
	return fs
}

func TestLoadEmpty(t *testing.T) {
	fs := tempStorage(t)
	tasks, err := fs.LoadTasks()
	if err != nil {
		t.Fatalf("LoadTasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected 0 tasks, got %d", len(tasks))
	}
}

func TestSaveAndLoad(t *testing.T) {
	fs := tempStorage(t)
	input := []*TaskData{
		{ID: "1", Name: "backup", Command: "echo hi", Type: "interval", IntervalMs: 60000, Status: "running"},
		{ID: "2", Name: "cleanup", Command: "rm -rf /tmp/old", Type: "cron", CronExpr: "0 3 * * *", Status: "paused"},
	}
	if err := fs.SaveTasks(input); err != nil {
		t.Fatalf("SaveTasks: %v", err)
	}
	loaded, err := fs.LoadTasks()
	if err != nil {
		t.Fatalf("LoadTasks: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(loaded))
	}
	if loaded[0].Name != "backup" || loaded[1].Name != "cleanup" {
		t.Fatalf("unexpected task names: %s, %s", loaded[0].Name, loaded[1].Name)
	}
}

func TestSaveTask_Insert(t *testing.T) {
	fs := tempStorage(t)
	task := &TaskData{ID: "1", Name: "test", Command: "echo 1", Type: "interval", Status: "running"}
	if err := fs.SaveTask(task); err != nil {
		t.Fatalf("SaveTask: %v", err)
	}
	loaded, _ := fs.LoadTasks()
	if len(loaded) != 1 || loaded[0].ID != "1" {
		t.Fatalf("expected 1 task with ID 1, got %d tasks", len(loaded))
	}
}

func TestSaveTask_Update(t *testing.T) {
	fs := tempStorage(t)
	task := &TaskData{ID: "1", Name: "old", Command: "echo 1", Type: "interval", Status: "running"}
	fs.SaveTask(task)

	task.Name = "updated"
	if err := fs.SaveTask(task); err != nil {
		t.Fatalf("SaveTask update: %v", err)
	}
	loaded, _ := fs.LoadTasks()
	if len(loaded) != 1 {
		t.Fatalf("expected 1 task, got %d", len(loaded))
	}
	if loaded[0].Name != "updated" {
		t.Fatalf("expected name 'updated', got '%s'", loaded[0].Name)
	}
}

func TestDeleteTask(t *testing.T) {
	fs := tempStorage(t)
	fs.SaveTask(&TaskData{ID: "1", Name: "a"})
	fs.SaveTask(&TaskData{ID: "2", Name: "b"})

	if err := fs.DeleteTask("1"); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	loaded, _ := fs.LoadTasks()
	if len(loaded) != 1 || loaded[0].ID != "2" {
		t.Fatalf("expected only task 2 remaining, got %v", loaded)
	}
}

func TestDeleteTask_NonExistent(t *testing.T) {
	fs := tempStorage(t)
	if err := fs.DeleteTask("nonexistent"); err != nil {
		t.Fatalf("DeleteTask should not error for missing ID: %v", err)
	}
}

func TestAtomicWrite_NoPartialFile(t *testing.T) {
	fs := tempStorage(t)
	task := &TaskData{ID: "1", Name: "test", Command: "echo 1"}
	fs.SaveTask(task)

	// Verify no .tmp file remains
	tmpPath := filepath.Join(filepath.Dir(fs.path), "tasks.json.tmp")
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("temp file should not exist after successful write")
	}
}

func TestSaveTask_WithLastResult(t *testing.T) {
	fs := tempStorage(t)
	task := &TaskData{
		ID:      "1",
		Name:    "test",
		Command: "echo ok",
		LastResult: &ResultData{
			Success: true,
			Message: "done",
		},
	}
	fs.SaveTask(task)
	loaded, _ := fs.LoadTasks()
	if loaded[0].LastResult == nil {
		t.Fatal("expected LastResult to be persisted")
	}
	if !loaded[0].LastResult.Success || loaded[0].LastResult.Message != "done" {
		t.Fatalf("unexpected LastResult: %+v", loaded[0].LastResult)
	}
}
