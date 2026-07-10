package main

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"time"
)

const (
	defaultReadinessTimeout  = 10 * time.Second
	defaultReadinessCacheTTL = 2 * time.Minute
	readinessStatusReady     = "ready"
	readinessStatusNotReady  = "not_ready"
)

type readinessReport struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
	Errors []string          `json:"errors,omitempty"`
}

type readinessCheckFunc func(context.Context) error

type readinessCheckSpec struct {
	name string
	fn   readinessCheckFunc
}

type readinessReporter interface {
	Check(context.Context) readinessReport
}

type cachedReadinessChecker struct {
	mu       sync.Mutex
	now      func() time.Time
	timeout  time.Duration
	cacheTTL time.Duration
	checks   []readinessCheckSpec

	hasCache   bool
	cacheUntil time.Time
	cache      readinessReport

	running bool
	waitCh  chan struct{}
	last    readinessReport
}

func newCachedReadinessChecker(now func() time.Time, timeout, cacheTTL time.Duration, checks []readinessCheckSpec) *cachedReadinessChecker {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if timeout <= 0 {
		timeout = defaultReadinessTimeout
	}
	if cacheTTL <= 0 {
		cacheTTL = defaultReadinessCacheTTL
	}
	return &cachedReadinessChecker{
		now:      now,
		timeout:  timeout,
		cacheTTL: cacheTTL,
		checks:   append([]readinessCheckSpec(nil), checks...),
	}
}

type storageReadiness interface {
	CheckContainer(context.Context, string) error
	CheckUserDelegationCredential(context.Context) error
}

func newDefaultReadinessChecker(cfg Config, blob storageReadiness, vi videoIndexerReadiness, planner EditPlanner, lookPath func(string) (string, error)) readinessReporter {
	checks := []readinessCheckSpec{
		{name: "config", fn: func(context.Context) error { return cfg.Validate() }},
		{name: "storage.staging", fn: func(ctx context.Context) error {
			if blob == nil {
				return errors.New("storage is not configured")
			}
			return blob.CheckContainer(ctx, cfg.StagingContainer)
		}},
		{name: "storage.jobs", fn: func(ctx context.Context) error {
			if blob == nil {
				return errors.New("storage is not configured")
			}
			return blob.CheckContainer(ctx, cfg.JobContainer)
		}},
		{name: "storage.user_delegation", fn: func(ctx context.Context) error {
			if blob == nil {
				return errors.New("storage is not configured")
			}
			return blob.CheckUserDelegationCredential(ctx)
		}},
		{name: "video_indexer.account", fn: func(ctx context.Context) error {
			if vi == nil {
				return errors.New("video indexer is not configured")
			}
			_, err := vi.ResolveAccount(ctx)
			return err
		}},
		{name: "video_indexer.account_access_token", fn: func(ctx context.Context) error {
			if vi == nil {
				return errors.New("video indexer is not configured")
			}
			_, err := vi.AccountAccessToken(ctx)
			return err
		}},
		{name: "agent", fn: func(context.Context) error {
			if planner == nil {
				return errors.New("edit planner is not configured")
			}
			return nil
		}},
		{name: "ffmpeg", fn: func(ctx context.Context) error {
			return checkBinaryReady(ctx, lookPath, "ffmpeg")
		}},
		{name: "ffprobe", fn: func(ctx context.Context) error {
			return checkBinaryReady(ctx, lookPath, "ffprobe")
		}},
	}
	return newCachedReadinessChecker(func() time.Time { return time.Now().UTC() }, defaultReadinessTimeout, defaultReadinessCacheTTL, checks)
}

func (c *cachedReadinessChecker) Check(ctx context.Context) readinessReport {
	if c == nil {
		return readinessReport{
			Status: readinessStatusNotReady,
			Checks: map[string]string{"config": readinessStatusNotReady},
			Errors: []string{"config"},
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	now := c.now()
	c.mu.Lock()
	if c.hasCache && now.Before(c.cacheUntil) {
		report := cloneReadinessReport(c.cache)
		c.mu.Unlock()
		return report
	}
	if c.running {
		waitCh := c.waitCh
		c.mu.Unlock()
		select {
		case <-waitCh:
			c.mu.Lock()
			report := cloneReadinessReport(c.last)
			c.mu.Unlock()
			if report.Status == "" {
				return readinessReport{
					Status: readinessStatusNotReady,
					Checks: map[string]string{"readiness": readinessStatusNotReady},
					Errors: []string{"readiness"},
				}
			}
			return report
		case <-ctx.Done():
			return readinessReport{
				Status: readinessStatusNotReady,
				Checks: map[string]string{"readiness": readinessStatusNotReady},
				Errors: []string{"readiness.timeout"},
			}
		}
	}
	c.running = true
	c.waitCh = make(chan struct{})
	waitCh := c.waitCh
	c.mu.Unlock()

	report := c.run(ctx)

	c.mu.Lock()
	c.running = false
	c.last = cloneReadinessReport(report)
	if report.Status == readinessStatusReady {
		c.hasCache = true
		c.cache = cloneReadinessReport(report)
		c.cacheUntil = c.now().Add(c.cacheTTL)
	} else {
		c.hasCache = false
		c.cache = readinessReport{}
		c.cacheUntil = time.Time{}
	}
	close(waitCh)
	c.mu.Unlock()

	return cloneReadinessReport(report)
}

func (c *cachedReadinessChecker) run(ctx context.Context) readinessReport {
	report := readinessReport{
		Status: readinessStatusReady,
		Checks: make(map[string]string, len(c.checks)),
	}
	for _, check := range c.checks {
		status := readinessStatusReady
		if check.fn == nil {
			status = readinessStatusNotReady
			report.Errors = append(report.Errors, check.name)
			report.Checks[check.name] = status
			report.Status = readinessStatusNotReady
			continue
		}
		if err := check.fn(ctx); err != nil {
			status = readinessStatusNotReady
			report.Errors = append(report.Errors, check.name)
		}
		report.Checks[check.name] = status
		if status != readinessStatusReady {
			report.Status = readinessStatusNotReady
		}
	}
	if len(report.Errors) == 0 {
		report.Status = readinessStatusReady
	}
	return report
}

func checkBinaryReady(ctx context.Context, lookPath func(string) (string, error), binary string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	_, err := lookPath(binary)
	return err
}

func cloneReadinessReport(report readinessReport) readinessReport {
	cloned := readinessReport{
		Status: report.Status,
	}
	if len(report.Checks) > 0 {
		cloned.Checks = make(map[string]string, len(report.Checks))
		for k, v := range report.Checks {
			cloned.Checks[k] = v
		}
	} else {
		cloned.Checks = map[string]string{}
	}
	if len(report.Errors) > 0 {
		cloned.Errors = append([]string(nil), report.Errors...)
	}
	return cloned
}

type videoIndexerReadiness interface {
	ResolveAccount(context.Context) (ResolvedVideoIndexerAccount, error)
	AccountAccessToken(context.Context) (string, error)
}
