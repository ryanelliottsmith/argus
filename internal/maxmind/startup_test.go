package maxmind

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var requiredEditions = []string{"GeoLite2-ASN", "GeoLite2-City"}

func archiveFixture(t *testing.T, ed string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "enrich", "testdata", ed+".mmdb"))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: ed + "_release/" + ed + ".mmdb", Mode: 0644, Size: int64(len(b))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func startupServer(t *testing.T, h func(http.ResponseWriter, *http.Request)) Config {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(h))
	t.Cleanup(s.Close)
	return Config{Dir: t.TempDir(), AccountID: "test-account", LicenseKey: "test-secret", Permalinks: map[string]string{"GeoLite2-ASN": s.URL + "/GeoLite2-ASN", "GeoLite2-City": s.URL + "/GeoLite2-City"}}
}

func TestStartupDownloadsPairAndThrottlesRestart(t *testing.T) {
	bodies := map[string][]byte{}
	for _, ed := range requiredEditions {
		bodies["/"+ed] = archiveFixture(t, ed)
	}
	requests := 0
	cfg := startupServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		a, k, ok := r.BasicAuth()
		if !ok || a != "test-account" || k != "test-secret" {
			t.Error("missing credentials")
		}
		w.Header().Set("ETag", "release-1")
		w.Header().Set("Last-Modified", "Wed, 01 Jan 2025 00:00:00 GMT")
		if r.Method == http.MethodGet {
			_, _ = w.Write(bodies[r.URL.Path])
		}
	})
	var logs bytes.Buffer
	logf := func(format string, args ...any) { fmt.Fprintf(&logs, format+"\n", args...) }
	if err := SyncAtStartup(context.Background(), cfg, logf); err != nil {
		t.Fatal(err)
	}
	if strings.Count(logs.String(), "installed validated database") != 2 || !strings.Contains(logs.String(), "startup databases ready") {
		t.Fatal("startup must report successful installs and readiness")
	}
	if requests != 2 {
		t.Fatalf("first startup requests=%d want 2 GETs", requests)
	}
	for _, ed := range requiredEditions {
		if _, err := os.Stat(filepath.Join(cfg.Dir, ed+".mmdb")); err != nil {
			t.Fatal(err)
		}
	}
	requests = 0
	if err := SyncAtStartup(context.Background(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	if requests != 0 {
		t.Fatalf("restart requests=%d want 0", requests)
	}
}

func TestStartupDueChecksReleaseMetadata(t *testing.T) {
	bodies := map[string][]byte{}
	for _, ed := range requiredEditions {
		bodies["/"+ed] = archiveFixture(t, ed)
	}
	heads, gets := 0, 0
	release := "Wed, 01 Jan 2025 00:00:00 GMT"
	tag := "release-1"
	cfg := startupServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Last-Modified", release)
		w.Header().Set("ETag", tag)
		if r.Method == http.MethodHead {
			heads++
		} else {
			gets++
			_, _ = w.Write(bodies[r.URL.Path])
		}
	})
	cfg.CheckEvery = time.Nanosecond
	if err := SyncAtStartup(context.Background(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	heads, gets = 0, 0
	if err := SyncAtStartup(context.Background(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	if heads != 2 || gets != 0 {
		t.Fatalf("unchanged: HEAD=%d GET=%d", heads, gets)
	}
	heads, gets = 0, 0
	release = "Thu, 02 Jan 2025 00:00:00 GMT"
	tag = "release-2"
	if err := SyncAtStartup(context.Background(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	if heads != 2 || gets != 2 {
		t.Fatalf("changed: HEAD=%d GET=%d", heads, gets)
	}
}

func TestStartupFailedUpdatesKeepValidPairAndRetry(t *testing.T) {
	for _, failure := range []string{"unauthorized", "corrupt-mmdb", "truncated-gzip", "checksum", "status"} {
		t.Run(failure, func(t *testing.T) {
			bodies := map[string][]byte{}
			for _, ed := range requiredEditions {
				bodies["/"+ed] = archiveFixture(t, ed)
			}
			failing := false
			heads, gets := 0, 0
			tag := "one"
			cfg := startupServer(t, func(w http.ResponseWriter, r *http.Request) {
				if failing && failure == "unauthorized" {
					w.WriteHeader(401)
					return
				}
				w.Header().Set("ETag", tag)
				if r.Method == http.MethodHead {
					heads++
					return
				}
				gets++
				b := bodies[r.URL.Path]
				if failing {
					switch failure {
					case "status":
						w.WriteHeader(503)
						return
					case "corrupt-mmdb":
						var out bytes.Buffer
						gz := gzip.NewWriter(&out)
						tw := tar.NewWriter(gz)
						_ = tw.WriteHeader(&tar.Header{Name: "bad.mmdb", Mode: 0644, Size: 3})
						_, _ = tw.Write([]byte("bad"))
						_ = tw.Close()
						_ = gz.Close()
						b = out.Bytes()
					case "truncated-gzip":
						b = b[:len(b)-8]
					case "checksum":
						b = append([]byte(nil), b...)
						b[len(b)-8] ^= 255
					}
				}
				_, _ = w.Write(b)
			})
			cfg.CheckEvery = time.Nanosecond
			if err := SyncAtStartup(context.Background(), cfg, nil); err != nil {
				t.Fatal(err)
			}
			old := map[string][]byte{}
			for _, ed := range requiredEditions {
				old[ed], _ = os.ReadFile(filepath.Join(cfg.Dir, ed+".mmdb"))
			}
			failing = true
			tag = "two"
			logs := ""
			logger := func(f string, a ...any) { logs += fmt.Sprintf(f, a...) }
			for attempt := 0; attempt < 2; attempt++ {
				heads, gets = 0, 0
				if err := SyncAtStartup(context.Background(), cfg, logger); err != nil {
					t.Fatalf("valid cached pair must survive: %v", err)
				}
				if failure != "unauthorized" && (heads != 2 || gets != 2) {
					t.Fatalf("failure must remain due: HEAD=%d GET=%d", heads, gets)
				}
			}
			if logs == "" {
				t.Error("failure was not logged")
			}
			for _, ed := range requiredEditions {
				b, _ := os.ReadFile(filepath.Join(cfg.Dir, ed+".mmdb"))
				if !bytes.Equal(b, old[ed]) {
					t.Errorf("%s replaced by bad download", ed)
				}
			}
		})
	}
}

func TestStartupMissingPairFatal(t *testing.T) {
	cfg := startupServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) })
	if err := SyncAtStartup(context.Background(), cfg, nil); err == nil {
		t.Fatal("missing pair must fail")
	}
	cfg.AccountID = ""
	cfg.LicenseKey = ""
	if err := SyncAtStartup(context.Background(), cfg, nil); err == nil {
		t.Fatal("missing credentials must fail")
	}
}

func TestStartupInvalidDatabaseBypassesRecentCheck(t *testing.T) {
	bodies := map[string][]byte{}
	for _, ed := range requiredEditions {
		bodies["/"+ed] = archiveFixture(t, ed)
	}
	gets := 0
	cfg := startupServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets++
			_, _ = w.Write(bodies[r.URL.Path])
		}
	})
	if err := SyncAtStartup(context.Background(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	for _, damage := range []string{"missing", "invalid"} {
		p := filepath.Join(cfg.Dir, "GeoLite2-ASN.mmdb")
		if damage == "missing" {
			_ = os.Remove(p)
		} else {
			_ = os.WriteFile(p, []byte("invalid"), 0644)
		}
		gets = 0
		if err := SyncAtStartup(context.Background(), cfg, nil); err != nil {
			t.Fatal(err)
		}
		if gets != 1 {
			t.Fatalf("%s repair GET=%d", damage, gets)
		}
	}
}

func TestStartupPersistsHEADValidatorsOnlyAfterSuccessfulInstall(t *testing.T) {
	heads, gets := 0, 0
	cfg := startupServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			heads++
			w.Header().Set("ETag", "head-release")
			return
		}
		gets++
		_, _ = w.Write(archiveFixture(t, strings.TrimPrefix(r.URL.Path, "/")))
	})
	cfg.CheckEvery = time.Nanosecond
	if err := SyncAtStartup(context.Background(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	// No validators accompanied the initial GET, so conservatively refresh once.
	if err := SyncAtStartup(context.Background(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	heads, gets = 0, 0
	if err := SyncAtStartup(context.Background(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	if heads != 2 || gets != 0 {
		t.Fatalf("HEAD=%d GET=%d: successful install must retain HEAD validators", heads, gets)
	}
}

func TestStartupMissingCredentialsWarnEvenWhenCheckRecent(t *testing.T) {
	cfg := startupServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archiveFixture(t, strings.TrimPrefix(r.URL.Path, "/")))
	})
	if err := SyncAtStartup(context.Background(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	cfg.LicenseKey = ""
	logs := ""
	if err := SyncAtStartup(context.Background(), cfg, func(f string, a ...any) { logs += fmt.Sprintf(f, a...) }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs, "missing account ID or license key") {
		t.Fatal("missing credential warning omitted")
	}
}

func TestStartupRejectsNonSuccessStatus(t *testing.T) {
	for _, status := range []int{300, 304, 401, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			heads, gets := 0, 0
			failing := false
			cfg := startupServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodHead {
					heads++
				}
				if failing {
					w.WriteHeader(status)
					return
				}
				gets++
				w.Header().Set("ETag", "one")
				_, _ = w.Write(archiveFixture(t, strings.TrimPrefix(r.URL.Path, "/")))
			})
			cfg.CheckEvery = time.Nanosecond
			if err := SyncAtStartup(context.Background(), cfg, nil); err != nil {
				t.Fatal(err)
			}
			failing = true
			heads, gets = 0, 0
			logs := ""
			if err := SyncAtStartup(context.Background(), cfg, func(f string, a ...any) { logs += fmt.Sprintf(f, a...) }); err != nil {
				t.Fatal(err)
			}
			if heads != 2 || gets != 0 || !strings.Contains(logs, fmt.Sprintf("status %d", status)) {
				t.Fatalf("HEAD=%d GET=%d logs=%s", heads, gets, logs)
			}
		})
	}
}

func TestStartupRedirectDoesNotForwardCredentials(t *testing.T) {
	bodies := map[string][]byte{}
	for _, ed := range requiredEditions {
		bodies["/"+ed] = archiveFixture(t, ed)
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("credentials leaked to redirected origin")
		}
		_, _ = w.Write(bodies[r.URL.Path])
	}))
	defer target.Close()
	cfg := startupServer(t, func(w http.ResponseWriter, r *http.Request) {
		a, k, ok := r.BasicAuth()
		if !ok || a != "test-account" || k != "test-secret" {
			t.Error("initial origin missing credentials")
		}
		http.Redirect(w, r, target.URL+r.URL.Path+"?signature=private-signature", http.StatusFound)
	})
	if err := SyncAtStartup(context.Background(), cfg, nil); err != nil {
		t.Fatal(err)
	}
}

func TestStartupErrorsNeverExposeURLsOrSecrets(t *testing.T) {
	cfg := startupServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/download?signature=private-signature", http.StatusFound)
	})
	logs := ""
	err := SyncAtStartup(context.Background(), cfg, func(f string, a ...any) { logs += fmt.Sprintf(f, a...) })
	if err == nil {
		t.Fatal("missing pair should fail")
	}
	logs += err.Error()
	for _, secret := range []string{"http://", "signature", "private-signature", cfg.AccountID, cfg.LicenseKey} {
		if strings.Contains(logs, secret) {
			t.Errorf("unsafe logs contain %q: %s", secret, logs)
		}
	}
	for _, ed := range requiredEditions {
		cfg.Permalinks[ed] = "https://test-account:test-secret@bad.invalid/%zz?signature=private-signature"
	}
	logs = ""
	_ = SyncAtStartup(context.Background(), cfg, func(f string, a ...any) { logs += fmt.Sprintf(f, a...) })
	for _, secret := range []string{"https://", "signature", "test-account", "test-secret"} {
		if strings.Contains(logs, secret) {
			t.Errorf("unsafe malformed URL log contains %q", secret)
		}
	}
}

func TestStartupValidCacheWithoutCredentials(t *testing.T) {
	requests := 0
	cfg := startupServer(t, func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(401) })
	for _, ed := range requiredEditions {
		b, err := os.ReadFile(filepath.Join("..", "enrich", "testdata", ed+".mmdb"))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(cfg.Dir, ed+".mmdb"), b, 0644); err != nil {
			t.Fatal(err)
		}
	}
	cfg.AccountID = ""
	cfg.LicenseKey = ""
	logs := ""
	if err := SyncAtStartup(context.Background(), cfg, func(f string, a ...any) { logs += fmt.Sprintf(f, a...) }); err != nil {
		t.Fatal(err)
	}
	if requests != 0 || !strings.Contains(logs, "missing account ID or license key") {
		t.Fatalf("requests=%d logs=%s", requests, logs)
	}
}

func TestStartupPreseedComparesReleaseNotFileModificationTime(t *testing.T) {
	// A fresh filesystem mtime must not masquerade as the database release.
	gets, heads := 0, 0
	build, err := databaseRelease(filepath.Join("..", "enrich", "testdata", "GeoLite2-City.mmdb"), "GeoLite2-City")
	if err != nil {
		t.Fatal(err)
	}
	release := build.Add(24 * time.Hour).Format(http.TimeFormat)
	cfg := startupServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Last-Modified", release)
		if r.Method == http.MethodHead {
			heads++
			return
		}
		gets++
		_, _ = w.Write(archiveFixture(t, strings.TrimPrefix(r.URL.Path, "/")))
	})
	for _, ed := range requiredEditions {
		b, err := os.ReadFile(filepath.Join("..", "enrich", "testdata", ed+".mmdb"))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(cfg.Dir, ed+".mmdb"), b, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := SyncAtStartup(context.Background(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	if heads != 2 || gets != 2 {
		t.Fatalf("preseed HEAD=%d GET=%d", heads, gets)
	}
}
