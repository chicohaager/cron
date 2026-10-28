// Command cron is the ZimaOS task scheduler backend: it keeps the task
// registry, runs commands on their schedule, serves the JSON API behind the
// ZimaOS gateway and installs its own boot watchdog units.
package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/chicohaager/cron/internal/storage"
	"github.com/chicohaager/lintux-modkit/appdata"
	"github.com/chicohaager/lintux-modkit/auth"
	"github.com/chicohaager/lintux-modkit/gateway"
	"github.com/chicohaager/lintux-modkit/httpx"
	"github.com/chicohaager/lintux-modkit/notify"
	"github.com/chicohaager/lintux-modkit/watchdog"
)

const (
	version            = "0.3.5"
	defaultStoragePath = "/DATA/AppData/cron"
	defaultRuntimePath = "/var/run/casaos" // where ZimaOS' gateway announces itself (management.url)
	maxRequestBody     = 1 << 20           // 1 MB
	maxTasks           = 500
	maxConcurrentRuns  = 10
	storageAttempts    = 30 // /DATA is a late bind mount on ZimaOS; wait for it
	storageRetryDelay  = 2 * time.Second
	storageRecheck     = 30 * time.Second // after the wait: how often a refused storage is tried again
)

var (
	tasks = map[string]*Task{}
	mu    sync.RWMutex
	store storage.Storage
	// storageErr is why the storage is refused after the wait, nil while it is open.
	// The server stays up and answers 503 storage_refused with the reason: 0.3.4
	// exited instead, systemd restarted it 1038 times on a box whose /DATA belonged
	// to uid 999, no job ran and nothing showed why (nas-main, 2026-10-28).
	storageErr atomic.Pointer[string]
	startTime  = time.Now()
	execSem    = make(chan struct{}, maxConcurrentRuns)
)

func main() {
	log.Printf("[cron] starting v%s", version)
	notify.AppName = "cron"
	if err := watchdog.Install(watchdog.Options{Service: "cron", Binary: "/usr/bin/cron"}); err != nil {
		log.Printf("[cron] %v", err)
	}

	path := defaultStoragePath
	if envPath := os.Getenv("CRON_DATA_PATH"); envPath != "" {
		path = envPath
	}
	if fs, err := openStorage(path, storageAttempts, storageRetryDelay); err != nil {
		refuseStorage(path, err)
		go recheckStorage(path, storageRecheck)
	} else {
		useStorage(fs)
	}

	runtimePath := defaultRuntimePath
	if envPath := os.Getenv("CASAOS_RUNTIME_PATH"); envPath != "" {
		runtimePath = envPath
	}

	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", "0"))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("[cron] listening on http://%s", listener.Addr())

	// The route is registered in the background so the server is up even
	// while the gateway is still starting (it needs ~45 s after boot).
	go registerRoute(runtimePath, routePrefix, "http://"+listener.Addr().String())

	verifier := newVerifier(runtimePath)
	srv := &http.Server{
		Handler:           withStatic(httpx.CSRF(storageGate(newMux(verifier.Middleware)))),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go shutdownOnSignal(srv)
	if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	log.Printf("[cron] server stopped")
}

// openAt opens the storage after the folder check: /DATA/AppData is 0777 without
// the sticky bit on ZimaOS 1.8 and the jobs run as root (KB §79.11) — appdata.Secure
// sets the sticky bit and refuses a folder that is a symlink, not root's or
// writable by others.
func openAt(path string) (storage.Storage, error) {
	if err := appdata.Secure(path); err != nil {
		return nil, err
	}
	return storage.NewFileStorage(path)
}

// openStorage retries until the data directory is writable; on ZimaOS the
// service can start before /DATA is mounted.
func openStorage(path string, attempts int, delay time.Duration) (storage.Storage, error) {
	for i := 1; ; i++ {
		fs, err := openAt(path)
		if err == nil {
			return fs, nil
		}
		if i >= attempts {
			return nil, err
		}
		log.Printf("[cron] storage not ready (attempt %d/%d): %v", i, attempts, err)
		time.Sleep(delay)
	}
}

// useStorage loads the tasks from fs; until then no task is scheduled.
func useStorage(fs storage.Storage) {
	store = fs
	if err := loadPersistedTasks(); err != nil {
		log.Printf("[cron] Warning: failed to load tasks: %v", err)
	}
	storageErr.Store(nil)
}

func refuseStorage(path string, err error) {
	msg := err.Error()
	storageErr.Store(&msg)
	log.Printf("[cron] ERROR: storage at %s refused, no task runs until it is fixed (tried again every %s): %v", path, storageRecheck, err)
}

// recheckStorage tries the refused storage again until it opens, so a fixed
// folder works without a restart.
func recheckStorage(path string, every time.Duration) {
	for {
		time.Sleep(every)
		fs, err := openAt(path)
		if err != nil {
			if old := storageErr.Load(); old == nil || *old != err.Error() {
				refuseStorage(path, err)
			}
			continue
		}
		useStorage(fs)
		log.Printf("[cron] storage at %s available again, tasks loaded", path)
		return
	}
}

// storageGate answers every API route but /health with 503 storage_refused
// while the storage is refused (the handlers need it).
func storageGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if msg := storageErr.Load(); msg != nil && strings.HasPrefix(r.URL.Path, routePrefix+"/") && r.URL.Path != routePrefix+"/health" {
			writeError(w, http.StatusServiceUnavailable, "storage_refused", *msg)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// newVerifier builds the session-token verifier. CRON_DISABLE_AUTH=1 is a
// development switch for running without a ZimaOS user-service; it is
// logged loudly and never set by the shipped unit.
func newVerifier(runtimePath string) *auth.Verifier {
	if os.Getenv("CRON_DISABLE_AUTH") == "1" {
		log.Printf("[cron] WARNING: authentication disabled by CRON_DISABLE_AUTH — every LAN client may run root commands")
		return auth.Disabled()
	}
	return auth.NewVerifier(auth.JWKSResolver(runtimePath))
}

func shutdownOnSignal(srv *http.Server) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	sig := <-sigCh
	log.Printf("[cron] received %v, shutting down", sig)
	mu.Lock()
	for _, t := range tasks {
		clearSchedule(t)
	}
	mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("[cron] shutdown: %v", err)
	}
}

// registerRoute tells the gateway where we listen. It runs in the
// background so the server is up while the gateway is still starting
// (measured after a reboot of a 1.7.1 box: ~45 s), and retries for two
// minutes because a route posted before the gateway listens is lost.
func registerRoute(runtimePath, path, target string) {
	for i := 1; i <= 60; i++ {
		err := gateway.Register(context.Background(), runtimePath, path, target, 10*time.Second)
		if err == nil {
			log.Printf("[cron] Gateway route registered: %s -> %s", path, target)
			return
		}
		log.Printf("[cron] Gateway not ready (attempt %d/60): %v", i, err)
		time.Sleep(2 * time.Second)
	}
	log.Printf("[cron] WARNING: Could not register gateway route after 60 attempts (2 min)")
}
