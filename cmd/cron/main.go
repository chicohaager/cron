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
	"sync"
	"syscall"
	"time"

	"github.com/chicohaager/cron/internal/auth"
	conf "github.com/chicohaager/cron/internal/config"
	svc "github.com/chicohaager/cron/internal/service"
	"github.com/chicohaager/cron/internal/storage"
)

const (
	version            = "0.3.0"
	defaultStoragePath = "/DATA/AppData/cron"
	maxRequestBody     = 1 << 20 // 1 MB
	maxTasks           = 500
	maxConcurrentRuns  = 10
	storageAttempts    = 30 // /DATA is a late bind mount on ZimaOS; wait for it
	storageRetryDelay  = 2 * time.Second
)

var (
	tasks     = map[string]*Task{}
	mu        sync.RWMutex
	store     storage.Storage
	startTime = time.Now()
	execSem   = make(chan struct{}, maxConcurrentRuns)
)

func main() {
	log.Printf("[cron] starting v%s", version)
	installWatchdog()

	store = openStorage()
	if err := loadPersistedTasks(); err != nil {
		log.Printf("[cron] Warning: failed to load tasks: %v", err)
	}

	runtimePath := conf.CommonInfo.RuntimePath
	if envPath := os.Getenv("CASAOS_RUNTIME_PATH"); envPath != "" {
		runtimePath = envPath
	}

	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", "0"))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("[cron] listening on http://%s", listener.Addr())

	// The route is registered in the background so the server is up even
	// while the gateway is still starting (it needs ~45 s after boot).
	svc.RegisterRouteAsync(runtimePath, routePrefix, "http://"+listener.Addr().String())

	verifier := newVerifier(runtimePath)
	srv := &http.Server{
		Handler:           withStatic(withCSRF(newMux(verifier.Middleware))),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go shutdownOnSignal(srv)
	if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	log.Printf("[cron] server stopped")
}

// openStorage retries until the data directory is writable; on ZimaOS the
// service can start before /DATA is mounted.
func openStorage() storage.Storage {
	path := defaultStoragePath
	if envPath := os.Getenv("CRON_DATA_PATH"); envPath != "" {
		path = envPath
	}
	for i := 1; ; i++ {
		fs, err := storage.NewFileStorage(path)
		if err == nil {
			return fs
		}
		if i >= storageAttempts {
			log.Fatalf("[cron] storage at %s not available after %d attempts: %v", path, i, err)
		}
		log.Printf("[cron] storage not ready (attempt %d/%d): %v", i, storageAttempts, err)
		time.Sleep(storageRetryDelay)
	}
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
