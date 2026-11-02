package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// Run the actual entrypoint in a child so a startup panic, os.Exit, or global
// background goroutine cannot kill or contaminate the test runner. All data is
// disposable and every optional network integration is disabled.
func TestMainWithoutBGP(t *testing.T) {
	for _, tc := range []struct {
		name            string
		listenerEnabled bool
		startupUpdate   bool
		missingDBs      bool
	}{
		{name: "listener_disabled"},
		{name: "bgp_not_in_enrichment", listenerEnabled: true},
		{name: "startup_update_cached_offline", startupUpdate: true},
		{name: "startup_update_missing_fails", startupUpdate: true, missingDBs: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			work := t.TempDir()
			geoDir := filepath.Join(work, "geoip")
			if err := os.Mkdir(geoDir, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"GeoLite2-ASN.mmdb", "GeoLite2-City.mmdb"} {
				if tc.missingDBs {
					continue
				}
				data, err := os.ReadFile(filepath.Join("..", "..", "internal", "enrich", "testdata", name))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(geoDir, name), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			if err := listener.Close(); err != nil {
				t.Fatal(err)
			}
			cfgPath := filepath.Join(work, "config.yaml")
			// BGP must also stay off when its listener flag is set but bgp is
			// absent from the enrichment list. No peer is configured or contacted.
			cfg := fmt.Sprintf(`enrich: geoip
maxmind:
  enabled: false
  db_path: %q
bgp:
  bgp_listener:
    enabled: %t
collectors: {}
api:
  listen_address: 127.0.0.1
  port: %d
  mcp_enabled: false
`, geoDir, tc.listenerEnabled, port)
			if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestMainProcess$")
			cmd.Dir = work
			cmd.Env = append(os.Environ(), "ARGUS_TEST_MAIN_CONFIG="+cfgPath,
				"MAXMIND_UPDATE_ON_START="+strconv.FormatBool(tc.startupUpdate),
				"MAXMIND_ACCOUNT_ID=", "MAXMIND_LICENSE_KEY=")
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			waited := false
			go func() { done <- cmd.Wait() }()
			defer func() {
				if !waited {
					cancel()
					<-done
				}
			}()

			client := &http.Client{Timeout: 200 * time.Millisecond, Transport: &http.Transport{}}
			defer client.CloseIdleConnections()
			tick := time.NewTicker(20 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case err := <-done:
					waited = true
					if tc.missingDBs && err != nil && bytes.Contains(output.Bytes(), []byte("startup database sync failed")) {
						return // missing credentials and no usable cache must fail before readiness
					}
					t.Fatalf("entrypoint exited before API became ready: %v\n%s", err, output.String())
				case <-ctx.Done():
					cancel()
					<-done
					waited = true
					t.Fatalf("API never became ready: %v\n%s", ctx.Err(), output.String())
				case <-tick.C:
					resp, err := client.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/status")
					if err != nil {
						continue
					}
					var status map[string]bool
					decodeErr := json.NewDecoder(resp.Body).Decode(&status)
					resp.Body.Close()
					if resp.StatusCode != http.StatusOK || decodeErr != nil || !status["infoip"] || status["bgp"] {
						t.Fatalf("unexpected status: HTTP %d, data=%v, decode=%v", resp.StatusCode, status, decodeErr)
					}
					if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
						t.Fatal(err)
					}
					err = <-done
					waited = true
					if err != nil {
						t.Fatalf("entrypoint did not shut down cleanly: %v\n%s", err, output.String())
					}
					if tc.startupUpdate && !bytes.Contains(output.Bytes(), []byte("[maxmind] startup update check")) {
						t.Fatal("startup updater was not invoked before the API became ready")
					}
					return
				}
			}
		})
	}
}

func TestMainProcess(t *testing.T) {
	if cfg := os.Getenv("ARGUS_TEST_MAIN_CONFIG"); cfg != "" {
		os.Args = []string{"argus", "-c", cfg}
		main()
		os.Exit(0)
	}
}
