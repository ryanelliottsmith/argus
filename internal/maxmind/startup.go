package maxmind

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang"
)

// This sidecar intentionally does not share the legacy background loop's state.
type startupEdition struct {
	Checked  time.Time `json:"checked"`
	ETag     string    `json:"etag,omitempty"`
	Modified time.Time `json:"last_modified,omitempty"`
}

var startupMu sync.Mutex
var startupEditions = []string{"GeoLite2-ASN", "GeoLite2-City"}

func responseMetadata(h http.Header) startupEdition {
	s := startupEdition{ETag: h.Get("ETag")}
	s.Modified, _ = http.ParseTime(h.Get("Last-Modified"))
	if s.Modified.IsZero() {
		if m := dispDate.FindStringSubmatch(h.Get("Content-Disposition")); len(m) == 2 {
			s.Modified, _ = time.Parse("20060102", m[1])
		}
	}
	return s
}

func databaseRelease(path, edition string) (time.Time, error) {
	db, err := maxminddb.Open(path)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid or missing MMDB")
	}
	defer db.Close()
	// GeoIP2 City is schema-compatible with GeoLite2 City (also used by the
	// official synthetic test fixture).
	compatibleCity := edition == "GeoLite2-City" && db.Metadata.DatabaseType == "GeoIP2-City"
	if db.Metadata.DatabaseType != edition && !compatibleCity {
		return time.Time{}, fmt.Errorf("unexpected MMDB database type")
	}
	if err = db.Verify(); err != nil {
		return time.Time{}, fmt.Errorf("invalid MMDB contents")
	}
	return time.Unix(int64(db.Metadata.BuildEpoch), 0).UTC(), nil
}

// SyncAtStartup prepares a valid ASN/City pair before callers open MMDB readers.
// Enabled controls only the legacy background loop. Editions cannot omit the pair.
// Failed refreshes are tolerated only if both installed databases remain valid.
func SyncAtStartup(ctx context.Context, cfg Config, logf func(string, ...any)) error {
	startupMu.Lock()
	defer startupMu.Unlock()
	if logf == nil {
		logf = func(string, ...any) {}
	}
	u := New(cfg)
	if err := os.MkdirAll(u.cfg.Dir, 0755); err != nil {
		return fmt.Errorf("maxmind: cannot create database directory")
	}
	path := filepath.Join(u.cfg.Dir, ".startup-state.json")
	st := map[string]startupEdition{}
	if b, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(b, &st) != nil {
			st = nil
		}
	}
	if st == nil {
		st = map[string]startupEdition{}
	}
	changed := false
	for _, ed := range startupEditions {
		old := st[ed]
		build, validErr := databaseRelease(filepath.Join(u.cfg.Dir, ed+".mmdb"), ed)
		var err error
		if u.cfg.AccountID == "" || u.cfg.LicenseKey == "" {
			err = fmt.Errorf("missing account ID or license key")
		} else if u.cfg.Permalinks[ed] == "" {
			err = fmt.Errorf("missing download permalink")
		}
		checkAge := time.Since(old.Checked)
		if err == nil && validErr == nil && !old.Checked.IsZero() && checkAge >= 0 && checkAge < u.cfg.CheckEvery {
			logf("[maxmind] %s: using cached database; check not due", ed)
			continue
		}
		var remote startupEdition
		if err == nil && validErr == nil {
			modified, etag, headErr := u.head(ctx, u.cfg.Permalinks[ed])
			remote = startupEdition{Modified: modified, ETag: etag}
			err = headErr
			if err == nil {
				baseline := old.Modified
				if baseline.IsZero() {
					baseline = build
				}
				unchanged := etag != "" && old.ETag != "" && etag == old.ETag
				if etag == "" || old.ETag == "" {
					unchanged = !modified.IsZero() && !modified.After(baseline)
				}
				if unchanged {
					old.Checked = time.Now()
					old.Modified = modified
					old.ETag = etag
					st[ed] = old
					changed = true
					logf("[maxmind] %s: remote release unchanged", ed)
					continue
				}
			}
		}
		if err == nil {
			var installed startupEdition
			err = u.downloadAndInstallMetadata(ctx, u.cfg.Permalinks[ed], ed, &installed)
			if err == nil {
				// GET metadata wins if present; only associate HEAD validators
				// with the installed file after the atomic install succeeded.
				if installed.ETag == "" {
					installed.ETag = remote.ETag
				}
				if installed.Modified.IsZero() {
					installed.Modified = remote.Modified
				}
				installed.Checked = time.Now()
				st[ed] = installed
				changed = true
				logf("[maxmind] %s: installed validated database", ed)
				continue
			}
		}
		logf("[maxmind] startup refresh failed for %s: %v; retaining valid cached databases if available", ed, err)
	}
	if changed {
		b, err := json.MarshalIndent(st, "", "  ")
		if err == nil {
			err = os.WriteFile(path+".tmp", b, 0600)
		}
		if err == nil {
			err = os.Rename(path+".tmp", path)
		}
		if err != nil {
			logf("[maxmind] could not persist startup check state; next startup will recheck")
		}
	}
	for _, ed := range startupEditions {
		if _, err := databaseRelease(filepath.Join(u.cfg.Dir, ed+".mmdb"), ed); err != nil {
			return fmt.Errorf("maxmind: startup requires a valid %s database; refresh failed (see startup log)", ed)
		}
	}
	logf("[maxmind] startup databases ready")
	return nil
}
