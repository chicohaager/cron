package service

import (
  "log"
  "sync"
  "time"

  "github.com/IceWhaleTech/CasaOS-Common/external"
  "github.com/IceWhaleTech/CasaOS-Common/model"
)

var (
  Gateway external.ManagementService
  mu      sync.Mutex
)

// RegisterRouteAsync connects to the CasaOS gateway and registers a route,
// all in a background goroutine. The HTTP server starts immediately without waiting.
func RegisterRouteAsync(runtimePath, path, target string) {
  if runtimePath == "" {
    log.Printf("[cron] No runtime path, skipping gateway registration")
    return
  }
  go func() {
    log.Printf("[cron] Starting gateway registration (path=%s target=%s runtime=%s)", path, target, runtimePath)
    for i := 0; i < 60; i++ {
      ms, err := external.NewManagementService(runtimePath)
      if err != nil {
        log.Printf("[cron] Gateway not ready (attempt %d/60): %v", i+1, err)
        time.Sleep(2 * time.Second)
        continue
      }
      if err := ms.CreateRoute(&model.Route{Path: path, Target: target}); err != nil {
        log.Printf("[cron] Route registration failed (attempt %d/60): %v", i+1, err)
        time.Sleep(2 * time.Second)
        continue
      }
      mu.Lock()
      Gateway = ms
      mu.Unlock()
      log.Printf("[cron] Gateway route registered: %s -> %s", path, target)
      return
    }
    log.Printf("[cron] WARNING: Could not register gateway route after 60 attempts (2 min)")
  }()
}
