package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/chicohaager/cron/internal/storage"
	"github.com/chicohaager/lintux-modkit/notify"
	"github.com/chicohaager/lintux-modkit/schedule"
)

// Result codes are stable identifiers the UI translates; Message carries the
// raw command output or error text and is never localised.
const (
	codeCompleted         = "completed"
	codeExitError         = "exit_error"
	codeTimeout           = "timeout"
	codeSkippedDependency = "skipped_dependency"
	codeSkippedRunning    = "skipped_running"
	codeSkippedQueueFull  = "skipped_queue_full"
)

const (
	defaultTimeout    = 2 * time.Minute
	defaultRetryDelay = 10 * time.Second
	defaultMaxLogs    = 100
	maxMessageLen     = 4000
	queueWait         = 30 * time.Second
)

// Locking convention for everything in this file: startSchedule,
// clearSchedule, toggleTask and scheduleCronNext mutate timer state and MUST
// be called with mu held. Timer callbacks and the interval goroutine take mu
// themselves and never hold it while a command runs.

// startSchedule arms the task's timer or ticker. Caller holds mu.
func startSchedule(t *Task) {
	clearSchedule(t)
	if t.Type == "interval" {
		startIntervalSchedule(t)
		return
	}
	scheduleCronNext(t)
}

// startIntervalSchedule binds the ticker channel once, so the goroutine
// never reads t.ticker after clearSchedule has nilled it. Caller holds mu.
func startIntervalSchedule(t *Task) {
	if t.Interval <= 0 {
		// validation rejects this; the guard keeps a bad persisted value
		// from panicking NewTicker while the caller holds mu
		log.Printf("task %s: interval %s is not positive, not scheduled", t.ID, t.Interval)
		return
	}
	ticker := time.NewTicker(t.Interval)
	done := make(chan struct{})
	t.ticker, t.done = ticker, done
	t.NextRunAt = time.Now().Add(t.Interval).UnixMilli()
	go func(tick <-chan time.Time) {
		for {
			select {
			case <-done:
				return
			case <-tick:
				if !isScheduled(t) {
					continue
				}
				runTaskOnce(t)
				mu.Lock()
				if t.done == done { // not rescheduled or cleared meanwhile
					t.NextRunAt = time.Now().Add(t.Interval).UnixMilli()
				}
				mu.Unlock()
				persistTask(t)
			}
		}
	}(ticker.C)
}

// scheduleCronNext arms a one-shot timer for the next matching minute.
// Caller holds mu. The generation counter lets the callback detect that the
// schedule was cleared or re-armed while the command was running, so a
// Pause/Resume during a long run cannot leave two timers alive.
func scheduleCronNext(t *Task) {
	next := schedule.Next(t.CronExpr, time.Now())
	if next.IsZero() {
		t.NextRunAt = 0
		return
	}
	t.NextRunAt = next.UnixMilli()
	t.gen++
	gen := t.gen
	t.timer = time.AfterFunc(time.Until(next), func() {
		if !isScheduled(t) {
			return
		}
		runTaskOnce(t)
		mu.Lock()
		if t.gen == gen && isRegisteredLocked(t) && t.Status == "running" {
			scheduleCronNext(t)
		}
		mu.Unlock()
		persistTask(t)
	})
}

// clearSchedule stops every timer the task owns. Caller holds mu.
func clearSchedule(t *Task) {
	t.gen++
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
	if t.retryTimer != nil {
		t.retryTimer.Stop()
		t.retryTimer = nil
	}
	t.CurrentRetry = 0
}

// toggleTask flips running/paused. Caller holds mu.
func toggleTask(t *Task) {
	if t.Status == "running" {
		t.Status = "paused"
		clearSchedule(t)
		t.NextRunAt = 0
		return
	}
	t.Status = "running"
	startSchedule(t)
}

// isRegisteredLocked reports whether t is still the live entry for its ID.
// A deleted task keeps its pointer alive inside pending callbacks; this is
// the check that stops those callbacks from resurrecting it. Caller holds mu.
func isRegisteredLocked(t *Task) bool {
	return tasks[t.ID] == t
}

// isScheduled is the callback-side guard: registered and not paused.
func isScheduled(t *Task) bool {
	mu.RLock()
	defer mu.RUnlock()
	return isRegisteredLocked(t) && t.Status == "running"
}

// recordSkip stores a non-execution outcome so it is visible in the UI and
// the history instead of only in the journal.
func recordSkip(t *Task, code string) {
	log.Printf("[cron] Task %s skipped: %s", t.ID, code)
	mu.Lock()
	// a tick skipped because the previous run is still going is worth a log
	// line, but the task's result stays the one the running attempt will
	// write — otherwise the list shows "skipped" while the task executes
	if code != codeSkippedRunning {
		t.LastResult = &Result{Success: false, Code: code}
	}
	appendLogLocked(t, LogEntry{Time: time.Now().UnixMilli(), Success: false, Code: code})
	mu.Unlock()
	persistTask(t)
	persistLogs(t)
}

// appendLogLocked prepends an entry and enforces the per-task cap. Caller holds mu.
func appendLogLocked(t *Task, e LogEntry) {
	t.logs = append([]LogEntry{e}, t.logs...)
	limit := t.MaxLogEntries
	if limit <= 0 {
		limit = defaultMaxLogs
	}
	if len(t.logs) > limit {
		t.logs = t.logs[:limit]
	}
}

// execSnapshot is the copy of task fields a run needs, taken under mu once
// so the command executes against a consistent view.
type execSnapshot struct {
	id, name, command string
	timeout           time.Duration
	retryCount        int
	retryDelay        time.Duration
	currentRetry      int
	env               map[string]string
	dependsOn         []string
	notifications     []notify.Config
}

func snapshotLocked(t *Task) execSnapshot {
	s := execSnapshot{
		id: t.ID, name: t.Name, command: t.Command,
		timeout:       time.Duration(t.TimeoutSec) * time.Second,
		retryCount:    t.RetryCount,
		retryDelay:    time.Duration(t.RetryDelaySec) * time.Second,
		currentRetry:  t.CurrentRetry,
		env:           make(map[string]string, len(t.Env)),
		dependsOn:     append([]string(nil), t.DependsOn...),
		notifications: append([]notify.Config(nil), t.Notifications...),
	}
	for k, v := range t.Env {
		s.env[k] = v
	}
	if s.timeout <= 0 {
		s.timeout = defaultTimeout
	}
	if s.retryDelay <= 0 {
		s.retryDelay = defaultRetryDelay
	}
	return s
}

// runTaskOnce executes the task's command one time, records the outcome,
// schedules a retry on failure and sends notifications on the final attempt.
// It is safe to call from any goroutine and must NOT be called with mu held.
func runTaskOnce(t *Task) {
	// Refuse to run a task that was deleted while a callback was pending, and
	// enforce the overlap rule: unless the task allows parallel runs, a tick
	// that arrives while the previous run is still going is skipped.
	mu.Lock()
	if !isRegisteredLocked(t) {
		mu.Unlock()
		return
	}
	if t.Executing && !t.AllowParallel {
		mu.Unlock()
		recordSkip(t, codeSkippedRunning)
		return
	}
	t.Executing = true
	snap := snapshotLocked(t)
	mu.Unlock()
	defer func() {
		mu.Lock()
		t.Executing = false
		mu.Unlock()
	}()

	if !canRunWithDeps(snap.dependsOn) {
		recordSkip(t, codeSkippedDependency)
		return
	}

	select {
	case execSem <- struct{}{}:
	case <-time.After(queueWait):
		recordSkip(t, codeSkippedQueueFull)
		return
	}
	defer func() { <-execSem }()

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), snap.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-lc", snap.command)
	// the timeout must take the shell's children with it: a command like
	// "rsync …" or "sleep 30" is a child of /bin/sh, and killing only the
	// shell leaves it running with the output pipes open, so Wait would
	// block until it ends by itself
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	if len(snap.env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range snap.env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	finished := time.Now()

	result := classify(err, ctx.Err(), stdout.String(), stderr.String(), snap.timeout)
	durationMs := finished.Sub(start).Milliseconds()

	mu.Lock()
	t.LastRunAt = finished.UnixMilli()
	t.LastResult = &result
	appendLogLocked(t, LogEntry{Time: t.LastRunAt, DurationMs: durationMs,
		Success: result.Success, Code: result.Code, Message: result.Message})
	retry := !result.Success && snap.retryCount > 0 && snap.currentRetry < snap.retryCount
	if retry {
		t.CurrentRetry++
		log.Printf("[cron] Task %s failed, retry %d/%d in %v", snap.id, t.CurrentRetry, snap.retryCount, snap.retryDelay)
		t.retryTimer = time.AfterFunc(snap.retryDelay, func() { runTaskOnce(t) })
	} else {
		t.CurrentRetry = 0
	}
	mu.Unlock()

	if !retry {
		sendNotifications(snap, result, durationMs)
	}
	persistTask(t)
	persistLogs(t)
}

// classify turns the process outcome into a Result. On success only stdout
// is kept (stderr is usually noise such as tar's "Removing leading /"); on
// failure both streams are combined so the diagnosis is complete.
func classify(runErr, ctxErr error, stdout, stderr string, timeout time.Duration) Result {
	if runErr == nil && ctxErr == nil {
		return Result{Success: true, Code: codeCompleted, Message: truncate(strings.TrimSpace(stdout))}
	}
	msg := strings.TrimSpace(stdout + "\n" + stderr)
	code := codeExitError
	if ctxErr == context.DeadlineExceeded {
		code = codeTimeout
		if msg == "" {
			msg = fmt.Sprintf("timeout after %s", timeout)
		}
	} else if msg == "" && runErr != nil {
		msg = runErr.Error()
	}
	return Result{Success: false, Code: code, Message: truncate(msg)}
}

func truncate(s string) string {
	if len(s) > maxMessageLen {
		return s[:maxMessageLen] + "..."
	}
	return s
}

// sendNotifications dispatches per-task channels plus the global Telegram
// setting; it only runs for the final attempt, never for a retried failure.
func sendNotifications(snap execSnapshot, result Result, durationMs int64) {
	taskInfo := notify.TaskInfo{ID: snap.id, Name: snap.name, Command: snap.command}
	resultInfo := notify.ResultInfo{Success: result.Success, Message: result.Message, DurationMs: durationMs}
	if len(snap.notifications) > 0 {
		notify.Send(snap.notifications, taskInfo, resultInfo)
	}
	if tg := getTelegramNotifyConfig(); tg != nil {
		notify.Send([]notify.Config{*tg}, taskInfo, resultInfo)
	}
}

// canRunWithDeps reports whether every dependency's last run succeeded.
// Missing dependencies are ignored so deleting a task never wedges others.
func canRunWithDeps(dependsOn []string) bool {
	if len(dependsOn) == 0 {
		return true
	}
	mu.RLock()
	defer mu.RUnlock()
	for _, id := range dependsOn {
		dep, ok := tasks[id]
		if !ok {
			continue
		}
		if dep.LastResult == nil || !dep.LastResult.Success {
			return false
		}
	}
	return true
}

// persistTask writes the task to storage unless it has been deleted in the
// meantime — a late retry or timer callback must not write a removed task
// back into tasks.json.
func persistTask(t *Task) {
	if store == nil {
		return
	}
	// the registry check and the save happen under one read lock: a DELETE
	// in between would otherwise let the save re-create the task on disk
	// (deleteTask takes the write lock and removes the file entry inside it)
	mu.RLock()
	defer mu.RUnlock()
	if !isRegisteredLocked(t) {
		return
	}
	if err := store.SaveTask(taskToData(t)); err != nil {
		log.Printf("[cron] Error persisting task %s: %v", t.ID, err)
	}
}

// persistLogs writes the task's run history to its own file, with the same
// deleted-task guard as persistTask.
func persistLogs(t *Task) {
	if store == nil {
		return
	}
	mu.RLock()
	live := isRegisteredLocked(t)
	var data []storage.LogEntryData
	if live {
		data = logsToData(t.logs)
	}
	mu.RUnlock()
	if !live {
		return
	}
	if err := store.SaveLogs(t.ID, data); err != nil {
		log.Printf("[cron] Error persisting logs of %s: %v", t.ID, err)
	}
}

// persistDelete removes a task and its history from storage (best-effort).
func persistDelete(id string) {
	if store == nil {
		return
	}
	if err := store.DeleteTask(id); err != nil {
		log.Printf("[cron] Error deleting task %s from storage: %v", id, err)
	}
	if err := store.DeleteLogs(id); err != nil {
		log.Printf("[cron] Error deleting logs of %s: %v", id, err)
	}
}
