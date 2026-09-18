package main

import (
	"os"
	"testing"
	"time"

	"github.com/chicohaager/cron/internal/storage"
)

// newTestStore points the package-level store at a throwaway directory and
// clears the task registry, so each test starts from an empty world.
func newTestStore(t *testing.T) *storage.FileStorage {
	t.Helper()
	fs, err := storage.NewFileStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store = fs
	mu.Lock()
	tasks = map[string]*Task{}
	mu.Unlock()
	return fs
}

func registerTask(t *testing.T, task *Task) {
	t.Helper()
	mu.Lock()
	tasks[task.ID] = task
	mu.Unlock()
}

func countStored(t *testing.T, fs *storage.FileStorage, id string) int {
	t.Helper()
	all, err := fs.LoadTasks()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, td := range all {
		if td.ID == id {
			n++
		}
	}
	return n
}

func waitFor(t *testing.T, cond func() bool, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// TestPauseResumeAgainstRunningTicker is audit probe B: toggling a 2 ms
// interval task fifty times used to race clearSchedule against the ticker
// goroutine and panic on a nil ticker. Run with -race.
func TestPauseResumeAgainstRunningTicker(t *testing.T) {
	newTestStore(t)
	task := &Task{ID: "probe1", Name: "probe", Command: "true", Type: "interval",
		Interval: 2 * time.Millisecond, Status: "running"}
	registerTask(t, task)
	mu.Lock()
	startSchedule(task)
	mu.Unlock()
	for i := 0; i < 50; i++ {
		time.Sleep(time.Millisecond)
		mu.Lock()
		toggleTask(task)
		mu.Unlock()
	}
	mu.Lock()
	clearSchedule(task)
	mu.Unlock()
	time.Sleep(20 * time.Millisecond)
}

// TestDeletedTaskIsNotResurrectedByRetry is audit finding 8: a pending retry
// must neither run the command of a deleted task nor write it back to disk.
func TestDeletedTaskIsNotResurrectedByRetry(t *testing.T) {
	fs := newTestStore(t)
	marker := t.TempDir() + "/ran"
	task := &Task{ID: "zombie", Name: "z", Type: "interval", Interval: time.Hour,
		Command: "touch " + marker + "; exit 1", RetryCount: 1, RetryDelaySec: 1, Status: "running"}
	registerTask(t, task)

	runTaskOnce(task) // fails, arms the retry timer
	if countStored(t, fs, task.ID) != 1 {
		t.Fatal("task should be persisted after the first run")
	}

	mu.Lock()
	clearSchedule(task)
	delete(tasks, task.ID)
	mu.Unlock()
	persistDelete(task.ID)
	fileRemoved := func() bool { return !fileExists(marker) }
	if !fileRemoved() {
		removeFile(marker)
	}

	time.Sleep(1500 * time.Millisecond) // past the retry delay
	if fileExists(marker) {
		t.Error("retry executed the command of a deleted task")
	}
	if n := countStored(t, fs, task.ID); n != 0 {
		t.Errorf("deleted task is back in storage (%d entries)", n)
	}
}

// TestDeleteDuringRunIsNotWrittenBack isolates the persistTask guard from
// the timer guard above: the run is already in flight when the task is
// deleted, so no timer is left to stop — only the registration check can
// keep the finished run from writing the task back to disk.
func TestDeleteDuringRunIsNotWrittenBack(t *testing.T) {
	fs := newTestStore(t)
	task := &Task{ID: "inflight", Name: "i", Type: "interval", Interval: time.Hour,
		Command: "sleep 0.3", Status: "running"}
	registerTask(t, task)
	done := make(chan struct{})
	go func() { runTaskOnce(task); close(done) }()
	waitFor(t, func() bool { mu.RLock(); defer mu.RUnlock(); return task.Executing }, time.Second)

	mu.Lock()
	clearSchedule(task)
	delete(tasks, task.ID)
	mu.Unlock()
	persistDelete(task.ID)
	<-done

	if n := countStored(t, fs, task.ID); n != 0 {
		t.Errorf("finished run wrote the deleted task back (%d entries)", n)
	}
}

// TestUnregisteredTaskDoesNotRun isolates the runTaskOnce entry guard: a
// pointer that is no longer in the registry must not execute its command.
func TestUnregisteredTaskDoesNotRun(t *testing.T) {
	newTestStore(t)
	marker := t.TempDir() + "/ran"
	task := &Task{ID: "ghost", Name: "g", Type: "interval", Interval: time.Hour,
		Command: "touch " + marker, Status: "running"}
	runTaskOnce(task) // never registered
	if fileExists(marker) {
		t.Error("unregistered task executed its command")
	}
}

// TestOverlapIsSkippedUnlessParallelAllowed pins the semantics of
// allow_parallel: a tick during a running command is recorded as skipped.
func TestOverlapIsSkippedUnlessParallelAllowed(t *testing.T) {
	newTestStore(t)
	task := &Task{ID: "slow", Name: "slow", Type: "interval", Interval: time.Hour,
		Command: "sleep 0.3", Status: "running"}
	registerTask(t, task)

	go runTaskOnce(task)
	waitFor(t, func() bool { mu.RLock(); defer mu.RUnlock(); return task.Executing }, time.Second)
	runTaskOnce(task) // second call while the first is still running

	mu.RLock()
	last := task.LastResult
	mu.RUnlock()
	if last == nil || last.Code != codeSkippedRunning {
		t.Fatalf("expected %s, got %+v", codeSkippedRunning, last)
	}
	waitFor(t, func() bool { mu.RLock(); defer mu.RUnlock(); return !task.Executing }, 2*time.Second)

	// With allow_parallel the second run executes instead of being skipped.
	task.AllowParallel = true
	task.Command = "true"
	runTaskOnce(task)
	mu.RLock()
	last = task.LastResult
	mu.RUnlock()
	if last == nil || last.Code != codeCompleted {
		t.Fatalf("expected %s, got %+v", codeCompleted, last)
	}
}

// TestClearedLogsStayCleared is audit finding 7: clearing logs must reach disk.
func TestClearedLogsStayCleared(t *testing.T) {
	fs := newTestStore(t)
	task := &Task{ID: "logs", Name: "l", Type: "interval", Interval: time.Hour,
		Command: "echo hi", Status: "running"}
	registerTask(t, task)
	runTaskOnce(task)

	mu.Lock()
	task.logs = nil
	mu.Unlock()
	persistTask(task)

	all, _ := fs.LoadTasks()
	for _, td := range all {
		if td.ID == task.ID && len(td.Logs) != 0 {
			t.Errorf("storage still holds %d log entries after clear", len(td.Logs))
		}
	}
}

// TestCronNextRunIsPersisted is finding N1: after a scheduled run the file
// must carry the *next* run time, not the one that just fired.
func TestCronScheduleRearmsWithSingleTimer(t *testing.T) {
	newTestStore(t)
	task := &Task{ID: "cron1", Name: "c", Type: "cron", CronExpr: "* * * * *",
		Command: "true", Status: "running"}
	registerTask(t, task)
	mu.Lock()
	startSchedule(task)
	first := task.NextRunAt
	toggleTask(task) // pause
	toggleTask(task) // resume: re-arms, generation moves on
	second := task.NextRunAt
	mu.Unlock()
	if first == 0 || second == 0 {
		t.Fatalf("next_run_at not set: %d %d", first, second)
	}
	mu.Lock()
	clearSchedule(task)
	if task.timer != nil || task.done != nil || task.retryTimer != nil {
		t.Error("clearSchedule left a timer behind")
	}
	mu.Unlock()
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
func removeFile(p string)      { _ = os.Remove(p) }
