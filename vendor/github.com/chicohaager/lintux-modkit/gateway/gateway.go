// Package gateway registers a module's route with the ZimaOS gateway.
//
// The gateway's management API is announced in <runtimePath>/management.url
// (measured on ZimaOS 1.7.1: /var/run/casaos/management.url holds
// "http://127.0.0.1:<port>"); POST /v1/gateway/routes with {path,target}
// answers 201 and from then on the gateway forwards <path> to <target>.
// This is the one call CasaOS-Common's external package was pulled in for —
// together with echo, jwt v3 and friends that no module here ever used.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Register makes the gateway forward path to target. The management URL
// file appears after the gateway starts; Register waits for it up to wait
// (a module started by systemd's watchdog before the gateway is the case
// that needs it), then posts once.
func Register(ctx context.Context, runtimePath, path, target string, wait time.Duration) error {
	base, err := ManagementURL(ctx, runtimePath, wait)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{"path": path, "target": target})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/gateway/routes", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("gateway routes: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("gateway refused route %s: HTTP %d", path, resp.StatusCode)
	}
	return nil
}

// ManagementURL reads the gateway's management address, waiting up to wait
// for the file to appear. The address is returned without a trailing slash.
func ManagementURL(ctx context.Context, runtimePath string, wait time.Duration) (string, error) {
	file := filepath.Join(runtimePath, "management.url")
	deadline := time.Now().Add(wait)
	for {
		raw, err := os.ReadFile(file)
		if err == nil {
			base := strings.TrimRight(strings.TrimSpace(string(raw)), "/")
			if base == "" {
				return "", errors.New("gateway management url file is empty")
			}
			return base, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("gateway management url: %w", err)
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("gateway management url %s not found within %s", file, wait)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
