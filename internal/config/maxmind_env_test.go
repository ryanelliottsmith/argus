package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaxMindCredentialsFromEnvironment(t *testing.T) {
	t.Setenv("MAXMIND_ACCOUNT_ID", "test-env-account")
	t.Setenv("MAXMIND_LICENSE_KEY", "test-env-license")
	cfg := loadFromYAML(t, `maxmind:
  account_id: yaml-account
  license_key: yaml-license
`)
	if cfg.MaxMind.AccountID != "test-env-account" || cfg.MaxMind.LicenseKey != "test-env-license" {
		t.Fatal("MaxMind environment credentials did not override YAML credentials")
	}
}

func TestMaxMindStartupEnvironmentRejectsInvalidBoolean(t *testing.T) {
	t.Setenv("MAXMIND_UPDATE_ON_START", "not-a-boolean")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("maxmind: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "MAXMIND_UPDATE_ON_START") {
		t.Fatal("expected a named configuration error for an invalid startup-update boolean")
	}
}

func TestMaxMindStartupDefaultsResolveBeforeGeoIPPaths(t *testing.T) {
	t.Setenv("MAXMIND_UPDATE_ON_START", "true")
	cfg := loadFromYAML(t, "maxmind: {}\n")
	if !cfg.MaxMind.UpdateOnStart || cfg.MaxMind.Enabled {
		t.Fatal("startup mode must enable only the startup updater, not the background loop")
	}
	if cfg.MaxMind.DBPath != "/var/lib/argus/maxmind" ||
		cfg.GeoIP.ASNDB != filepath.Join(cfg.MaxMind.DBPath, "GeoLite2-ASN.mmdb") ||
		cfg.GeoIP.CityDB != filepath.Join(cfg.MaxMind.DBPath, "GeoLite2-City.mmdb") {
		t.Fatal("startup defaults must be resolved before deriving local reader paths")
	}
	if len(cfg.MaxMind.Editions) != 2 {
		t.Fatal("startup mode must default to both required databases")
	}
}
