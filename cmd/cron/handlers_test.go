package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chicohaager/lintux-modkit/auth"
	"github.com/chicohaager/lintux-modkit/httpx"
	"github.com/chicohaager/lintux-modkit/notify"
)

// newTestServer serves the real route table without a session check, so
// the tests exercise exactly the handlers and middleware the product wires.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	newTestStore(t)
	srv := httptest.NewServer(httpx.CSRF(newMux(auth.Disabled().Middleware)))
	t.Cleanup(func() {
		srv.Close()
		mu.Lock()
		for _, task := range tasks {
			clearSchedule(task)
		}
		tasks = map[string]*Task{}
		mu.Unlock()
	})
	return srv
}

func call(t *testing.T, srv *httptest.Server, method, path string, body interface{}) (*http.Response, []byte) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if s, ok := body.(string); ok {
			buf.WriteString(s)
		} else if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, _ := http.NewRequest(method, srv.URL+path, &buf)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out bytes.Buffer
	_, _ = out.ReadFrom(resp.Body)
	return resp, out.Bytes()
}

func decode(t *testing.T, raw []byte, v interface{}) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
}

func createVia(t *testing.T, srv *httptest.Server, req taskRequest) taskView {
	t.Helper()
	resp, raw := call(t, srv, http.MethodPost, "/cron/tasks", req)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create %q: %d %s", req.Name, resp.StatusCode, raw)
	}
	var v taskView
	decode(t, raw, &v)
	return v
}

// Finding 6: the API must speak minutes and real milliseconds, not nanoseconds.
func TestIntervalUnitsInAPI(t *testing.T) {
	srv := newTestServer(t)
	v := createVia(t, srv, taskRequest{Name: "i", Command: "true", Type: "interval", IntervalMin: 30})
	if v.IntervalMin != 30 || v.IntervalMs != 30*60*1000 {
		t.Fatalf("interval_min=%d interval_ms=%d", v.IntervalMin, v.IntervalMs)
	}
}

// Finding 5: export → import must keep every task, interval ones included.
func TestExportImportRoundTrip(t *testing.T) {
	srv := newTestServer(t)
	createVia(t, srv, taskRequest{Name: "c1", Command: "true", Type: "cron", CronExpr: "0 3 * * 1", Priority: 7})
	createVia(t, srv, taskRequest{Name: "c2", Command: "true", Type: "cron", CronExpr: "0 5 1 * *"})
	createVia(t, srv, taskRequest{Name: "c3", Command: "true", Type: "cron", CronExpr: "*/15 * * * *", Tags: []string{"a"}})
	createVia(t, srv, taskRequest{Name: "i1", Command: "true", Type: "interval", IntervalMin: 45, Env: map[string]string{"K": "v"}})

	resp, raw := call(t, srv, http.MethodGet, "/cron/export", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("export: %d", resp.StatusCode)
	}
	var exported exportFile
	decode(t, raw, &exported)
	if len(exported.Tasks) != 4 || exported.Version != exportVersion {
		t.Fatalf("export = %+v", exported)
	}

	// wipe and re-import the very file
	mu.Lock()
	for _, task := range tasks {
		clearSchedule(task)
	}
	tasks = map[string]*Task{}
	mu.Unlock()
	resp, raw = call(t, srv, http.MethodPost, "/cron/import", string(raw))
	if resp.StatusCode != 200 {
		t.Fatalf("import: %d %s", resp.StatusCode, raw)
	}
	var result struct {
		Imported int
		Skipped  []map[string]string
	}
	decode(t, raw, &result)
	if result.Imported != 4 || len(result.Skipped) != 0 {
		t.Fatalf("import result %+v", result)
	}
	_, raw = call(t, srv, http.MethodGet, "/cron/tasks", nil)
	var list []taskView
	decode(t, raw, &list)
	found := map[string]taskView{}
	for _, v := range list {
		found[v.Name] = v
	}
	if got := found["i1"]; got.IntervalMin != 45 || got.Env["K"] != "v" || got.Status != statusPaused {
		t.Errorf("interval task after import: %+v", got)
	}
	if got := found["c1"]; got.Priority != 7 || got.CronExpr != "0 3 * * 1" {
		t.Errorf("cron task after import: %+v", got)
	}
}

// Rejected imports are named, never dropped silently.
func TestImportNamesSkippedTasks(t *testing.T) {
	srv := newTestServer(t)
	body := `[{"name":"ok","command":"true","type":"interval","interval_min":5},
	          {"name":"broken","command":"true","type":"cron","cron_expr":"99 * * * *"},
	          {"name":"","command":"true","type":"interval","interval_min":1}]`
	_, raw := call(t, srv, http.MethodPost, "/cron/import", body)
	var result struct {
		Imported int
		Skipped  []struct{ Name, Reason, Code string }
	}
	decode(t, raw, &result)
	if result.Imported != 1 || len(result.Skipped) != 2 {
		t.Fatalf("result %+v", result)
	}
	if result.Skipped[0].Name != "broken" || result.Skipped[0].Code != "cron_invalid" {
		t.Errorf("skipped[0] = %+v", result.Skipped[0])
	}
	if result.Skipped[1].Code != "name_required" {
		t.Errorf("skipped[1] = %+v", result.Skipped[1])
	}
}

func TestValidationErrorsCarryCodes(t *testing.T) {
	srv := newTestServer(t)
	cases := []struct {
		req  taskRequest
		code string
	}{
		{taskRequest{Command: "true", Type: "interval", IntervalMin: 1}, "name_required"},
		{taskRequest{Name: "x", Type: "interval", IntervalMin: 1}, "command_required"},
		{taskRequest{Name: "x", Command: "true", Type: "weekly"}, "type_invalid"},
		{taskRequest{Name: "x", Command: "true", Type: "interval"}, "interval_invalid"},
		{taskRequest{Name: "x", Command: "true", Type: "cron", CronExpr: "* * *"}, "cron_invalid"},
		{taskRequest{Name: "x", Command: "true", Type: "cron", CronExpr: "0 0 30 feb *"}, "cron_never_fires"},
		{taskRequest{Name: "x", Command: "true", Type: "interval", IntervalMin: 1, Priority: 11}, "priority_invalid"},
		{taskRequest{Name: "x", Command: "true", Type: "interval", IntervalMin: 1, DependsOn: []string{"nope"}}, "dependency_unknown"},
	}
	for _, c := range cases {
		resp, raw := call(t, srv, http.MethodPost, "/cron/tasks", c.req)
		var body map[string]string
		decode(t, raw, &body)
		if resp.StatusCode != 400 || body["code"] != c.code {
			t.Errorf("%+v: got %d %s, want 400 %s", c.req, resp.StatusCode, raw, c.code)
		}
	}
}

func TestDependencyCycleIsRejected(t *testing.T) {
	srv := newTestServer(t)
	a := createVia(t, srv, taskRequest{Name: "a", Command: "true", Type: "interval", IntervalMin: 1})
	b := createVia(t, srv, taskRequest{Name: "b", Command: "true", Type: "interval", IntervalMin: 1, DependsOn: []string{a.ID}})
	// a -> b would close the loop b -> a -> b
	resp, raw := call(t, srv, http.MethodPut, "/cron/tasks/"+a.ID,
		taskRequest{Name: "a", Command: "true", Type: "interval", IntervalMin: 1, DependsOn: []string{b.ID}})
	var body map[string]string
	decode(t, raw, &body)
	if resp.StatusCode != 400 || body["code"] != "dependency_cycle" {
		t.Fatalf("got %d %s", resp.StatusCode, raw)
	}
	resp, raw = call(t, srv, http.MethodPut, "/cron/tasks/"+a.ID,
		taskRequest{Name: "a", Command: "true", Type: "interval", IntervalMin: 1, DependsOn: []string{a.ID}})
	decode(t, raw, &body)
	if resp.StatusCode != 400 || body["code"] != "dependency_self" {
		t.Fatalf("self-dependency: got %d %s", resp.StatusCode, raw)
	}
}

func TestEditKeepsMaskedCredentialsAndReschedules(t *testing.T) {
	srv := newTestServer(t)
	v := createVia(t, srv, taskRequest{Name: "e", Command: "true", Type: "interval", IntervalMin: 10,
		Notifications: []notify.Config{{Enabled: true, Type: "email", Target: "a@example.com", SMTPHost: "mail", SMTPPass: "s3cret"}}})
	if v.Notifications[0].SMTPPass != maskedSecret {
		t.Fatalf("password not masked in response: %q", v.Notifications[0].SMTPPass)
	}
	// edit with the masked value echoed back, change the schedule
	resp, raw := call(t, srv, http.MethodPut, "/cron/tasks/"+v.ID, taskRequest{Name: "e2", Command: "true", Type: "cron", CronExpr: "0 4 * * 0",
		Notifications: []notify.Config{{Enabled: true, Type: "email", Target: "a@example.com", SMTPHost: "mail", SMTPPass: maskedSecret}}})
	if resp.StatusCode != 200 {
		t.Fatalf("edit: %d %s", resp.StatusCode, raw)
	}
	mu.RLock()
	task := tasks[v.ID]
	pass := task.Notifications[0].SMTPPass
	typ, next := task.Type, task.NextRunAt
	mu.RUnlock()
	if pass != "s3cret" {
		t.Errorf("stored password became %q", pass)
	}
	if typ != typeCron || next == 0 {
		t.Errorf("schedule not switched: type=%s next=%d", typ, next)
	}
}

func TestCrossOriginMutationIsRejected(t *testing.T) {
	srv := newTestServer(t)
	body, _ := json.Marshal(taskRequest{Name: "x", Command: "true", Type: "interval", IntervalMin: 1})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/cron/tasks", bytes.NewReader(body))
	req.Header.Set("Origin", "http://evil.example")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: %d, want 403", resp.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/cron/tasks", bytes.NewReader(body))
	req.Header.Set("Origin", "http://"+req.Host)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("same origin: %d, want 201", resp.StatusCode)
	}
}

func TestHealthIsOpenAndTasksListIsSortedByPriority(t *testing.T) {
	srv := newTestServer(t)
	createVia(t, srv, taskRequest{Name: "low", Command: "true", Type: "interval", IntervalMin: 1, Priority: 2})
	createVia(t, srv, taskRequest{Name: "high", Command: "true", Type: "interval", IntervalMin: 1, Priority: 9})
	createVia(t, srv, taskRequest{Name: "alpha", Command: "true", Type: "interval", IntervalMin: 1, Priority: 2})
	_, raw := call(t, srv, http.MethodGet, "/cron/tasks", nil)
	var list []taskView
	decode(t, raw, &list)
	names := []string{list[0].Name, list[1].Name, list[2].Name}
	if strings.Join(names, ",") != "high,alpha,low" {
		t.Fatalf("order %v", names)
	}
	resp, raw := call(t, srv, http.MethodGet, "/cron/health", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(raw), `"tasks_total":3`) {
		t.Fatalf("health: %d %s", resp.StatusCode, raw)
	}
}
