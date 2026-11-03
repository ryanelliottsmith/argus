package asnintel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultBaseURL       = "https://stat.ripe.net"
	defaultFreshTTL      = 15 * time.Minute
	defaultStaleTTL      = 2 * time.Hour
	defaultReqTimeout    = 3 * time.Second
	defaultRetryBudget   = 2 // extra attempts after first try
	defaultMaxListValues = 30
	defaultOpenFor       = 45 * time.Second
	// Large ASNs can return several MB of prefix timelines. Bound network input,
	// independently of the small display-list cap.
	maxResponseBytes = 16 << 20
)

type OptionalField[T any] struct {
	Present bool   `json:"present"`
	Source  string `json:"source,omitempty"`
	Value   T      `json:"value,omitempty"`
}

type ObservedPathsMetadata struct {
	PathCount  int        `json:"path_count"`
	UniqueHops int        `json:"unique_hops"`
	MinHops    int        `json:"min_hops"`
	MaxHops    int        `json:"max_hops"`
	Samples    [][]uint32 `json:"samples,omitempty"`
}

type Profile struct {
	ASN                    uint32                               `json:"asn"`
	Source                 string                               `json:"source"`
	FetchedAt              time.Time                            `json:"fetched_at"`
	Name                   OptionalField[string]                `json:"name"`
	Country                OptionalField[string]                `json:"country"`
	AnnouncedPrefixesCount OptionalField[int]                   `json:"announced_prefixes_count"`
	AnnouncedPrefixes      OptionalField[[]string]              `json:"announced_prefixes"`
	PeersCount             OptionalField[int]                   `json:"peers_count"`
	Peers                  OptionalField[[]uint32]              `json:"peers"`
	IXPresence             OptionalField[[]string]              `json:"ix_presence"`
	ObservedPaths          OptionalField[ObservedPathsMetadata] `json:"observed_paths"`
}

type cacheEntry struct {
	profile    Profile
	freshUntil time.Time
	expiresAt  time.Time
}

type CacheState struct {
	Hit   bool `json:"hit"`
	Stale bool `json:"stale"`
}

type ProviderStatus struct {
	Provider    string `json:"provider"`
	OK          bool   `json:"ok"`
	LatencyMS   int64  `json:"latency_ms"`
	Error       string `json:"error,omitempty"`
	Timeout     bool   `json:"timeout,omitempty"`
	CircuitOpen bool   `json:"circuit_open,omitempty"`
	FromCache   bool   `json:"from_cache,omitempty"`
	Stale       bool   `json:"stale,omitempty"`
}

type circuitState struct {
	consecutiveFailures int
	openUntil           time.Time
}

type Client struct {
	httpClient *http.Client
	baseURL    string
	freshTTL   time.Duration
	staleTTL   time.Duration
	timeout    time.Duration
	retries    int
	openFor    time.Duration

	mu         sync.RWMutex
	cache      map[uint32]cacheEntry
	breakers   map[string]*circuitState
	refreshing map[uint32]bool
}

func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{Timeout: defaultReqTimeout + time.Second},
		baseURL:    defaultBaseURL,
		freshTTL:   defaultFreshTTL,
		staleTTL:   defaultStaleTTL,
		timeout:    defaultReqTimeout,
		retries:    defaultRetryBudget,
		openFor:    defaultOpenFor,
		cache:      make(map[uint32]cacheEntry),
		breakers:   make(map[string]*circuitState),
		refreshing: make(map[uint32]bool),
	}
}

func (c *Client) GetProfile(ctx context.Context, asn uint32, observed [][]uint32) (Profile, CacheState, map[string]ProviderStatus, error) {
	now := time.Now().UTC()
	if p, cacheState, ok := c.getFromCache(asn, now); ok {
		if meta := buildObservedPaths(observed); meta.PathCount > 0 {
			p.ObservedPaths = OptionalField[ObservedPathsMetadata]{Present: true, Source: "flowstore:pathfinder", Value: meta}
		}
		status := map[string]ProviderStatus{
			"cache": {
				Provider:  "cache",
				OK:        true,
				FromCache: true,
				Stale:     cacheState.Stale,
			},
		}
		if cacheState.Stale {
			c.refreshAsync(asn, observed)
		}
		return p, cacheState, status, nil
	}

	return c.fetchProfile(ctx, asn, observed)
}

// fetchProfile deliberately bypasses cache reads so stale refresh does real I/O.
func (c *Client) fetchProfile(ctx context.Context, asn uint32, observed [][]uint32) (Profile, CacheState, map[string]ProviderStatus, error) {
	profile := Profile{ASN: asn, Source: "ripestat", FetchedAt: time.Now().UTC()}
	sourceStatus := map[string]ProviderStatus{
		"base":     {Provider: "ripestat:base", Error: "not_attempted"},
		"prefixes": {Provider: "ripestat:prefixes", Error: "not_attempted"},
		"peers":    {Provider: "ripestat:peers", Error: "not_attempted"},
	}

	baseData, baseStatus, err := c.fetchProviderJSON(ctx, "ripestat:base", fmt.Sprintf("%s/data/as-overview/data.json?resource=AS%d", c.baseURL, asn))
	sourceStatus["base"] = baseStatus
	if err != nil {
		if meta := buildObservedPaths(observed); meta.PathCount > 0 {
			profile.ObservedPaths = OptionalField[ObservedPathsMetadata]{Present: true, Source: "flowstore:pathfinder", Value: meta}
		}
		return profile, CacheState{}, sourceStatus, err
	}
	fillBaseFields(&profile, baseData)

	snapshot, _ := stringValue(mapValue(baseData, "data"), "query_endtime")
	prefixURL := fmt.Sprintf("%s/data/announced-prefixes/data.json?resource=AS%d&starttime=%s&endtime=%s", c.baseURL, asn, url.QueryEscape(snapshot), url.QueryEscape(snapshot))
	if prefixesData, status, err := c.fetchProviderJSON(ctx, "ripestat:prefixes", prefixURL); err == nil {
		fillPrefixes(&profile, prefixesData)
		sourceStatus["prefixes"] = status
	} else {
		sourceStatus["prefixes"] = status
	}
	if peersData, status, err := c.fetchProviderJSON(ctx, "ripestat:peers", fmt.Sprintf("%s/data/asn-neighbours/data.json?resource=AS%d", c.baseURL, asn)); err == nil {
		fillPeers(&profile, peersData)
		sourceStatus["peers"] = status
	} else {
		sourceStatus["peers"] = status
	}

	if meta := buildObservedPaths(observed); meta.PathCount > 0 {
		profile.ObservedPaths = OptionalField[ObservedPathsMetadata]{Present: true, Source: "flowstore:pathfinder", Value: meta}
	}
	profile.FetchedAt = time.Now().UTC()
	c.storeCache(profile)
	sourceStatus["cache"] = ProviderStatus{Provider: "cache", OK: true}
	return profile, CacheState{Hit: false, Stale: false}, sourceStatus, nil
}

func (c *Client) getFromCache(asn uint32, now time.Time) (Profile, CacheState, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.cache[asn]
	if !ok || now.After(entry.expiresAt) {
		return Profile{}, CacheState{}, false
	}
	state := CacheState{Hit: true, Stale: now.After(entry.freshUntil)}
	return entry.profile, state, true
}

func (c *Client) refreshAsync(asn uint32, observed [][]uint32) {
	c.mu.Lock()
	if c.refreshing[asn] {
		c.mu.Unlock()
		return
	}
	c.refreshing[asn] = true
	c.mu.Unlock()

	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.refreshing, asn)
			c.mu.Unlock()
		}()
		refreshCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, _, _, err := c.fetchProfile(refreshCtx, asn, observed); err != nil {
			log.Printf(`{"component":"asnintel","event":"async_refresh","asn":%d,"ok":false,"error":%q}`, asn, err.Error())
		}
	}()
}

func (c *Client) storeCache(p Profile) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.cache[p.ASN] = cacheEntry{
		profile:    p,
		freshUntil: now.Add(c.freshTTL),
		expiresAt:  now.Add(c.staleTTL),
	}
}

func (c *Client) fetchProviderJSON(ctx context.Context, provider string, url string) (map[string]interface{}, ProviderStatus, error) {
	var lastErr error
	attempts := c.retries + 1
	start := time.Now()
	status := ProviderStatus{Provider: provider}
	if c.isCircuitOpen(provider, start) {
		status.CircuitOpen = true
		status.Error = "circuit_open"
		status.LatencyMS = time.Since(start).Milliseconds()
		c.logProviderMetric(provider, status, 0)
		return nil, status, fmt.Errorf("provider %s circuit open", provider)
	}

	for attempt := 0; attempt < attempts; attempt++ {
		if ctx.Err() != nil {
			lastErr = ctx.Err()
			break
		}
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt) * 120 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				lastErr = ctx.Err()
			case <-timer.C:
			}
			if ctx.Err() != nil {
				break
			}
		}
		reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
		if err != nil {
			cancel()
			status.Error = err.Error()
			status.LatencyMS = time.Since(start).Milliseconds()
			c.logProviderMetric(provider, status, attempt+1)
			return nil, status, err
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			if reqCtx.Err() == context.DeadlineExceeded {
				status.Timeout = true
			}
			cancel()
			continue
		}
		var payload map[string]interface{}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		_ = resp.Body.Close()
		if readErr == nil && len(body) > maxResponseBytes {
			readErr = fmt.Errorf("RIPEstat response exceeds %d bytes", maxResponseBytes)
		}
		if readErr == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			readErr = json.Unmarshal(body, &payload)
		}
		if reqCtx.Err() == context.DeadlineExceeded {
			status.Timeout = true
		}
		cancel()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if err := validatePayload(provider, req.URL, payload); err != nil {
				lastErr = err
				break
			}
			status.OK = true
			status.LatencyMS = time.Since(start).Milliseconds()
			c.markProviderSuccess(provider)
			c.logProviderMetric(provider, status, attempt+1)
			return payload, status, nil
		}
		lastErr = fmt.Errorf("status %d", resp.StatusCode)
		if resp.StatusCode < 500 {
			break
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("unknown fetch error")
	}
	status.Error = lastErr.Error()
	if ctx.Err() == context.DeadlineExceeded {
		status.Timeout = true
	}
	status.LatencyMS = time.Since(start).Milliseconds()
	c.markProviderFailure(provider, time.Now())
	c.logProviderMetric(provider, status, attempts)
	return nil, status, lastErr
}

func (c *Client) isCircuitOpen(provider string, now time.Time) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	state, ok := c.breakers[provider]
	return ok && now.Before(state.openUntil)
}

func (c *Client) markProviderSuccess(provider string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.breakers[provider] = &circuitState{}
}

func (c *Client) markProviderFailure(provider string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state, ok := c.breakers[provider]
	if !ok {
		state = &circuitState{}
		c.breakers[provider] = state
	}
	state.consecutiveFailures++
	if state.consecutiveFailures >= 3 {
		state.openUntil = now.Add(c.openFor)
	}
}

func (c *Client) logProviderMetric(provider string, status ProviderStatus, attempts int) {
	log.Printf(
		`{"component":"asnintel","event":"provider_call","provider":%q,"ok":%t,"latency_ms":%d,"attempts":%d,"timeout":%t,"circuit_open":%t,"error":%q}`,
		provider,
		status.OK,
		status.LatencyMS,
		attempts,
		status.Timeout,
		status.CircuitOpen,
		status.Error,
	)
}

func validatePayload(provider string, requestURL *url.URL, payload map[string]interface{}) error {
	status, _ := stringValue(payload, "status")
	code, ok := intValue(payload, "status_code")
	data, object := payload["data"].(map[string]interface{})
	if status != "ok" || !ok || code != http.StatusOK || !object || data == nil {
		return fmt.Errorf("invalid RIPEstat envelope")
	}
	resource, ok := stringValue(data, "resource")
	if !ok || strings.TrimPrefix(resource, "AS") != strings.TrimPrefix(requestURL.Query().Get("resource"), "AS") {
		return fmt.Errorf("RIPEstat resource mismatch")
	}
	if provider == "ripestat:base" {
		if _, ok := stringValue(data, "holder"); !ok {
			return fmt.Errorf("invalid RIPEstat holder")
		}
		if _, err := dataTime(data, "query_endtime"); err != nil {
			return err
		}
	}
	if provider == "ripestat:prefixes" {
		snapshot, err := dataTime(data, "query_endtime")
		if err != nil {
			return err
		}
		requested, err := dataTime(map[string]interface{}{"time": requestURL.Query().Get("endtime")}, "time")
		if err != nil || !snapshot.Equal(requested) {
			return fmt.Errorf("RIPEstat prefix snapshot mismatch")
		}
		items, ok := data["prefixes"].([]interface{})
		if !ok {
			return fmt.Errorf("invalid RIPEstat prefixes")
		}
		for _, raw := range items {
			item, ok := raw.(map[string]interface{})
			if !ok {
				return fmt.Errorf("invalid RIPEstat prefix row")
			}
			prefix, ok := stringValue(item, "prefix")
			if _, err := netip.ParsePrefix(prefix); !ok || err != nil {
				return fmt.Errorf("invalid RIPEstat prefix")
			}
			timelines, ok := item["timelines"].([]interface{})
			if !ok || len(timelines) == 0 {
				return fmt.Errorf("invalid RIPEstat timelines")
			}
			for _, raw := range timelines {
				timeline, _ := raw.(map[string]interface{})
				start, err := dataTime(timeline, "starttime")
				if err != nil {
					return err
				}
				end, err := dataTime(timeline, "endtime")
				if err != nil {
					return err
				}
				if start.After(end) {
					return fmt.Errorf("invalid RIPEstat timeline range")
				}
			}
		}
	}
	if provider == "ripestat:peers" {
		items, ok := data["neighbours"].([]interface{})
		if !ok {
			return fmt.Errorf("invalid RIPEstat neighbours")
		}
		for _, raw := range items {
			item, ok := raw.(map[string]interface{})
			if !ok {
				return fmt.Errorf("invalid RIPEstat neighbour row")
			}
			asn, ok := intValue(item, "asn")
			kind, _ := stringValue(item, "type")
			if !ok || asn <= 0 || uint64(asn) > uint64(^uint32(0)) || (kind != "left" && kind != "right" && kind != "uncertain") {
				return fmt.Errorf("invalid RIPEstat neighbour")
			}
		}
	}
	return nil
}

func dataTime(data map[string]interface{}, key string) (time.Time, error) {
	value, ok := stringValue(data, key)
	if ok {
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05"} {
			if parsed, err := time.Parse(layout, value); err == nil {
				return parsed, nil
			}
		}
	}
	return time.Time{}, fmt.Errorf("invalid RIPEstat %s", key)
}

func fillBaseFields(out *Profile, payload map[string]interface{}) {
	data := mapValue(payload, "data")
	if name, ok := stringValue(data, "holder"); ok && name != "" {
		out.Name = OptionalField[string]{Present: true, Source: "ripestat", Value: name}
	}
}

func fillPrefixes(out *Profile, payload map[string]interface{}) {
	data := mapValue(payload, "data")
	items, _ := data["prefixes"].([]interface{})
	snapshot, _ := dataTime(data, "query_endtime")
	all := make([]string, 0, len(items))
	for _, raw := range items {
		item, _ := raw.(map[string]interface{})
		prefix, ok := stringValue(item, "prefix")
		p, err := netip.ParsePrefix(prefix)
		if !ok || err != nil {
			continue
		}
		timelines, _ := item["timelines"].([]interface{})
		for _, raw := range timelines {
			timeline, _ := raw.(map[string]interface{})
			start, _ := dataTime(timeline, "starttime")
			end, _ := dataTime(timeline, "endtime")
			if !snapshot.Before(start) && !snapshot.After(end) {
				all = append(all, p.Masked().String())
				break
			}
		}
	}
	all = uniqStrings(all)
	out.AnnouncedPrefixesCount = OptionalField[int]{Present: true, Source: "ripestat", Value: len(all)}
	if len(all) > defaultMaxListValues {
		all = all[:defaultMaxListValues]
	}
	out.AnnouncedPrefixes = OptionalField[[]string]{Present: true, Source: "ripestat", Value: all}
}

func fillPeers(out *Profile, payload map[string]interface{}) {
	data := mapValue(payload, "data")
	items, _ := data["neighbours"].([]interface{})
	peers := make([]uint32, 0, len(items))
	// Left/right are RIS-observed adjacencies, not contractual peer inventory.
	// Uncertain entries are collector-peering artifacts, not observed adjacencies.
	for _, raw := range items {
		item, _ := raw.(map[string]interface{})
		kind, _ := stringValue(item, "type")
		asn, ok := intValue(item, "asn")
		if ok && asn > 0 && uint64(asn) <= uint64(^uint32(0)) && (kind == "left" || kind == "right") {
			peers = append(peers, uint32(asn))
		}
	}
	peers = uniqUint32s(peers)
	out.PeersCount = OptionalField[int]{Present: true, Source: "ripestat", Value: len(peers)}
	if len(peers) > defaultMaxListValues {
		peers = peers[:defaultMaxListValues]
	}
	out.Peers = OptionalField[[]uint32]{Present: true, Source: "ripestat", Value: peers}
}

func buildObservedPaths(paths [][]uint32) ObservedPathsMetadata {
	if len(paths) == 0 {
		return ObservedPathsMetadata{}
	}
	seenHop := make(map[uint32]struct{})
	meta := ObservedPathsMetadata{PathCount: len(paths), MinHops: -1}
	for _, p := range paths {
		hops := len(p)
		if meta.MinHops == -1 || hops < meta.MinHops {
			meta.MinHops = hops
		}
		if hops > meta.MaxHops {
			meta.MaxHops = hops
		}
		for _, hop := range p {
			seenHop[hop] = struct{}{}
		}
		if len(meta.Samples) < 5 {
			meta.Samples = append(meta.Samples, p)
		}
	}
	meta.UniqueHops = len(seenHop)
	if meta.MinHops < 0 {
		meta.MinHops = 0
	}
	return meta
}

func mapValue(m map[string]interface{}, key string) map[string]interface{} {
	raw, ok := m[key]
	if !ok {
		return map[string]interface{}{}
	}
	mv, ok := raw.(map[string]interface{})
	if !ok {
		return map[string]interface{}{}
	}
	return mv
}

func stringValue(m map[string]interface{}, key string) (string, bool) {
	v, ok := m[key]
	if !ok || v == nil {
		return "", false
	}
	s, ok := v.(string)
	if ok {
		return strings.TrimSpace(s), true
	}
	return "", false
}

func intValue(m map[string]interface{}, key string) (int, bool) {
	v, ok := m[key]
	if !ok || v == nil {
		return 0, false
	}
	switch t := v.(type) {
	case float64:
		if t < 0 || t > float64(^uint32(0)) || float64(uint32(t)) != t {
			return 0, false
		}
		return int(t), true
	case int:
		return t, true
	case int64:
		return int(t), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

func uniqStrings(in []string) []string {
	if len(in) == 0 {
		return in
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func uniqUint32s(in []uint32) []uint32 {
	if len(in) == 0 {
		return in
	}
	seen := make(map[uint32]struct{}, len(in))
	out := make([]uint32, 0, len(in))
	for _, v := range in {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
