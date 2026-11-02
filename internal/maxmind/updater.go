package maxmind

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Config holds everything the updater needs. Populated from argus config.
type Config struct {
	Enabled         bool
	AccountID       string
	LicenseKey      string
	Editions        []string
	Dir             string
	CheckEvery      time.Duration
	MinAgeBetweenDL time.Duration
	HTTPTimeout     time.Duration
	Permalinks      map[string]string
}

// persisted state to avoid needless downloads
type state struct {
	LastChecked    time.Time            `json:"last_checked"`
	LastDownloaded map[string]time.Time `json:"last_downloaded"`
	LastRemoteDate map[string]time.Time `json:"last_remote_date"`
	ETags          map[string]string    `json:"etags,omitempty"`
}

var dispDate = regexp.MustCompile(`_(\d{8})\.`) // e.g. GeoLite2-City_20250925.tar.gz

type Updater struct {
	cfg   Config
	httpc *http.Client
	mu    sync.Mutex
}

func New(cfg Config) *Updater {
	if cfg.Dir == "" {
		cfg.Dir = "/var/lib/argus/maxmind"
	}
	if cfg.CheckEvery == 0 {
		cfg.CheckEvery = 24 * time.Hour
	}
	if cfg.MinAgeBetweenDL == 0 {
		cfg.MinAgeBetweenDL = 72 * time.Hour
	}
	if cfg.HTTPTimeout == 0 {
		cfg.HTTPTimeout = 30 * time.Second
	}
	if cfg.Permalinks == nil {
		cfg.Permalinks = defaultPermalinks()
	}
	return &Updater{
		cfg:   cfg,
		httpc: newHTTPClient(cfg.HTTPTimeout),
	}
}

func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		initial := via[0].URL
		if req.URL.Scheme != initial.Scheme || !strings.EqualFold(req.URL.Host, initial.Host) {
			req.Header.Del("Authorization")
		}
		return nil
	}}
}

func defaultPermalinks() map[string]string {
	return map[string]string{
		"GeoLite2-ASN":  "https://download.maxmind.com/geoip/databases/GeoLite2-ASN/download?suffix=tar.gz",
		"GeoLite2-City": "https://download.maxmind.com/geoip/databases/GeoLite2-City/download?suffix=tar.gz",
	}
}

// Run starts the periodic update loop. Runs checkOnce immediately on entry.
func (u *Updater) Run(ctx context.Context, logf func(string, ...any)) error {
	if !u.cfg.Enabled {
		return nil
	}
	if err := os.MkdirAll(u.cfg.Dir, 0o755); err != nil {
		return err
	}

	ticker := time.NewTicker(u.cfg.CheckEvery)
	defer ticker.Stop()

	u.checkOnce(ctx, logf)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			u.checkOnce(ctx, logf)
		}
	}
}

// EnsureDBs is a blocking preflight: if any edition's .mmdb is missing it
// downloads immediately, ignoring MinAgeBetweenDL. Safe to call at startup.
func EnsureDBs(cfg Config) error {
	if cfg.Permalinks == nil {
		cfg.Permalinks = defaultPermalinks()
	}
	if cfg.HTTPTimeout == 0 {
		cfg.HTTPTimeout = 60 * time.Second
	}

	u := &Updater{
		cfg:   cfg,
		httpc: newHTTPClient(cfg.HTTPTimeout),
	}

	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return fmt.Errorf("maxmind: mkdir %s: %w", cfg.Dir, err)
	}

	ctx := context.Background()
	var missing []string
	for _, ed := range cfg.Editions {
		path := filepath.Join(cfg.Dir, ed+".mmdb")
		if _, err := os.Stat(path); os.IsNotExist(err) {
			missing = append(missing, ed)
		}
	}
	if len(missing) == 0 {
		return nil // all present, nothing to do
	}

	for _, ed := range missing {
		url := cfg.Permalinks[ed]
		if url == "" {
			return fmt.Errorf("maxmind: no permalink configured for edition %s", ed)
		}
		if err := u.downloadAndInstall(ctx, url, ed); err != nil {
			return fmt.Errorf("maxmind: preflight download %s: %w", ed, err)
		}
	}
	return nil
}

func (u *Updater) checkOnce(ctx context.Context, logf func(string, ...any)) {
	u.mu.Lock()
	defer u.mu.Unlock()

	st, _ := u.loadState()
	if st == nil {
		st = &state{
			LastDownloaded: map[string]time.Time{},
			LastRemoteDate: map[string]time.Time{},
			ETags:          map[string]string{},
		}
	}

	for _, ed := range u.cfg.Editions {
		url := u.cfg.Permalinks[ed]
		if url == "" {
			logf("[maxmind] no permalink for edition %s; skipping", ed)
			continue
		}

		remoteDate, etag, err := u.head(ctx, url)
		if err != nil {
			logf("[maxmind] HEAD %s failed for %s: %v", url, ed, err)
			continue
		}

		st.LastChecked = time.Now()
		if !remoteDate.IsZero() {
			st.LastRemoteDate[ed] = remoteDate
		}
		if etag != "" {
			st.ETags[ed] = etag
		}

		lastDL := st.LastDownloaded[ed]
		minAgeOK := time.Since(lastDL) >= u.cfg.MinAgeBetweenDL
		newer := remoteDate.IsZero() || remoteDate.After(lastDL)

		if !minAgeOK || !newer {
			continue
		}

		logf("[maxmind] updating %s (lastDL=%v remoteDate=%v)", ed, lastDL, remoteDate)
		if err := u.downloadAndInstall(ctx, url, ed); err != nil {
			logf("[maxmind] download/install failed for %s: %v", ed, err)
			continue
		}
		st.LastDownloaded[ed] = time.Now()
	}

	if err := u.saveState(st); err != nil {
		logf("[maxmind] save state error: %v", err)
	}
}

func (u *Updater) head(ctx context.Context, url string) (time.Time, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("invalid HEAD request URL")
	}
	req.SetBasicAuth(u.cfg.AccountID, u.cfg.LicenseKey)
	resp, err := u.httpc.Do(req)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("HEAD request failed (network, redirect, timeout, or cancellation)")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return time.Time{}, "", fmt.Errorf("status %d", resp.StatusCode)
	}

	metadata := responseMetadata(resp.Header)
	return metadata.Modified, metadata.ETag, nil
}

func (u *Updater) downloadAndInstall(ctx context.Context, url, edition string) error {
	return u.downloadAndInstallMetadata(ctx, url, edition, nil)
}

func (u *Updater) downloadAndInstallMetadata(ctx context.Context, url, edition string, metadata *startupEdition) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("invalid GET request URL")
	}
	req.SetBasicAuth(u.cfg.AccountID, u.cfg.LicenseKey)

	resp, err := u.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("GET request failed (network, redirect, timeout, or cancellation)")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}

	gr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return err
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	outBase := edition + ".mmdb"
	tmpPath := ""
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("invalid database archive")
		}
		if !hdr.FileInfo().Mode().IsRegular() || !strings.HasSuffix(hdr.Name, ".mmdb") {
			continue
		}
		if tmpPath != "" {
			return fmt.Errorf("multiple MMDB files in archive")
		}
		tmp, err := os.CreateTemp(u.cfg.Dir, outBase+".tmp-*")
		if err != nil {
			return fmt.Errorf("cannot stage database")
		}
		tmpPath = tmp.Name()
		_, copyErr := io.Copy(tmp, tr)
		chmodErr := tmp.Chmod(0644)
		closeErr := tmp.Close()
		if copyErr != nil || chmodErr != nil || closeErr != nil {
			return fmt.Errorf("cannot write complete staged database")
		}
	}
	// tar EOF can occur before gzip's footer. Read to the physical end so a
	// truncated stream or failed CRC never replaces a last-known-good database.
	if _, err := io.Copy(io.Discard, gr); err != nil {
		return fmt.Errorf("invalid gzip checksum or truncated archive")
	}
	if tmpPath == "" {
		return fmt.Errorf("no MMDB found in database archive")
	}
	if _, err := databaseRelease(tmpPath, edition); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, filepath.Join(u.cfg.Dir, outBase)); err != nil {
		return fmt.Errorf("cannot install staged database")
	}
	if metadata != nil {
		*metadata = responseMetadata(resp.Header)
	}
	return nil
}

func (u *Updater) statePath() string { return filepath.Join(u.cfg.Dir, ".state.json") }

func (u *Updater) loadState() (*state, error) {
	b, err := os.ReadFile(u.statePath())
	if err != nil {
		return nil, err
	}
	var s state
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	if s.LastDownloaded == nil {
		s.LastDownloaded = map[string]time.Time{}
	}
	if s.LastRemoteDate == nil {
		s.LastRemoteDate = map[string]time.Time{}
	}
	if s.ETags == nil {
		s.ETags = map[string]string{}
	}
	return &s, nil
}

func (u *Updater) saveState(s *state) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := u.statePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, u.statePath())
}
