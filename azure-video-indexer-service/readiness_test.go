package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeReadinessReporter struct {
	report readinessReport
	calls  int
}

func (f *fakeReadinessReporter) Check(context.Context) readinessReport {
	f.calls++
	return cloneReadinessReport(f.report)
}

type fakeStorageReadiness struct {
	mu              sync.Mutex
	containerCalls  []string
	delegationCalls int
	containerErr    error
	delegationErr   error
}

func (f *fakeStorageReadiness) CheckContainer(_ context.Context, container string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.containerCalls = append(f.containerCalls, container)
	return f.containerErr
}

func (f *fakeStorageReadiness) CheckUserDelegationCredential(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delegationCalls++
	return f.delegationErr
}

type fakeVideoIndexerReadiness struct {
	mu           sync.Mutex
	resolveCalls int
	tokenCalls   int
	resolveErr   error
	tokenErr     error
}

func (f *fakeVideoIndexerReadiness) ResolveAccount(context.Context) (ResolvedVideoIndexerAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolveCalls++
	if f.resolveErr != nil {
		return ResolvedVideoIndexerAccount{}, f.resolveErr
	}
	return ResolvedVideoIndexerAccount{AccountID: "account-1", Location: "westus"}, nil
}

func (f *fakeVideoIndexerReadiness) AccountAccessToken(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenCalls++
	if f.tokenErr != nil {
		return "", f.tokenErr
	}
	return "discard-me", nil
}

type readinessEditPlanner struct{}

func (readinessEditPlanner) Plan(context.Context, string) (EditPlan, error) { return EditPlan{}, nil }

func validReadinessConfig() Config {
	return Config{
		ListenAddr:                 ":8080",
		APIKey:                     "test-api-key",
		StorageURL:                 "https://storage.example.com",
		StagingContainer:           "staging",
		JobContainer:               "jobs",
		GraphBaseURL:               "https://graph.microsoft.com/v1.0",
		VideoIndexerSubscriptionID: "sub-1",
		VideoIndexerResourceGroup:  "rg-1",
		VideoIndexerAccountName:    "acct-1",
		VideoIndexerTimeout:        time.Minute,
	}
}

func TestDefaultReadinessCheckerSuccessAndCache(t *testing.T) {
	now := time.Date(2026, 7, 10, 15, 4, 5, 0, time.UTC)
	storage := &fakeStorageReadiness{}
	vi := &fakeVideoIndexerReadiness{}
	checker := newDefaultReadinessChecker(validReadinessConfig(), storage, vi, readinessEditPlanner{}, func(name string) (string, error) {
		return `C:\tools\` + name + `.exe`, nil
	}).(*cachedReadinessChecker)
	checker.now = func() time.Time { return now }
	checker.cacheTTL = time.Minute

	first := checker.Check(context.Background())
	second := checker.Check(context.Background())

	if first.Status != readinessStatusReady || second.Status != readinessStatusReady {
		t.Fatalf("unexpected readiness: %#v %#v", first, second)
	}
	if len(first.Errors) != 0 || len(second.Errors) != 0 {
		t.Fatalf("expected no readiness errors: %#v %#v", first.Errors, second.Errors)
	}

	storage.mu.Lock()
	containerCalls := append([]string(nil), storage.containerCalls...)
	delegationCalls := storage.delegationCalls
	storage.mu.Unlock()
	vi.mu.Lock()
	resolveCalls := vi.resolveCalls
	tokenCalls := vi.tokenCalls
	vi.mu.Unlock()
	if len(containerCalls) != 2 {
		t.Fatalf("expected staging and jobs checks once, got %v", containerCalls)
	}
	if delegationCalls != 1 || resolveCalls != 1 || tokenCalls != 1 {
		t.Fatalf("expected cached readiness, got storage=%d resolve=%d token=%d", delegationCalls, resolveCalls, tokenCalls)
	}

	now = now.Add(2 * time.Minute)
	third := checker.Check(context.Background())
	if third.Status != readinessStatusReady {
		t.Fatalf("expected third readiness to stay ready: %#v", third)
	}
	storage.mu.Lock()
	delegationCalls = storage.delegationCalls
	storage.mu.Unlock()
	if delegationCalls != 2 {
		t.Fatalf("expected cache expiry to re-run checks, got %d user delegation checks", delegationCalls)
	}
}

func TestDefaultReadinessCheckerConfigAndFFmpegFailures(t *testing.T) {
	storage := &fakeStorageReadiness{}
	vi := &fakeVideoIndexerReadiness{}
	cfg := validReadinessConfig()
	cfg.StorageURL = ""
	checker := newDefaultReadinessChecker(cfg, storage, vi, readinessEditPlanner{}, func(name string) (string, error) {
		if name == "ffmpeg" {
			return "", errors.New("missing")
		}
		return `C:\tools\` + name + `.exe`, nil
	})

	report := checker.Check(context.Background())
	if report.Status != readinessStatusNotReady {
		t.Fatalf("expected not ready, got %#v", report)
	}
	if report.Checks["config"] != readinessStatusNotReady || report.Checks["ffmpeg"] != readinessStatusNotReady {
		t.Fatalf("expected config and ffmpeg failures, got %#v", report.Checks)
	}
	if !containsReadinessValue(report.Errors, "config") || !containsReadinessValue(report.Errors, "ffmpeg") {
		t.Fatalf("expected sanitized dependency names, got %#v", report.Errors)
	}
}

func TestReadinessCheckerSharesInFlightCheck(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls int
	var once sync.Once
	var mu sync.Mutex
	checker := newCachedReadinessChecker(func() time.Time { return time.Date(2026, 7, 10, 15, 4, 5, 0, time.UTC) }, time.Minute, time.Minute, []readinessCheckSpec{
		{name: "config", fn: func(context.Context) error {
			mu.Lock()
			calls++
			mu.Unlock()
			once.Do(func() { close(started) })
			<-release
			return nil
		}},
	})

	firstCh := make(chan readinessReport, 1)
	secondCh := make(chan readinessReport, 1)
	go func() { firstCh <- checker.Check(context.Background()) }()
	<-started
	go func() { secondCh <- checker.Check(context.Background()) }()
	time.Sleep(50 * time.Millisecond)
	close(release)

	first := <-firstCh
	second := <-secondCh
	if first.Status != readinessStatusReady || second.Status != readinessStatusReady {
		t.Fatalf("unexpected readiness reports: %#v %#v", first, second)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected a single underlying check, got %d", calls)
	}
}

func TestServerReadyUsesInjectedChecker(t *testing.T) {
	server := &Server{
		readiness: &fakeReadinessReporter{
			report: readinessReport{
				Status: readinessStatusNotReady,
				Checks: map[string]string{"ffmpeg": readinessStatusNotReady},
				Errors: []string{"ffmpeg"},
			},
		},
	}
	rr := httptest.NewRecorder()
	server.handleReady(rr, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status: %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "ffmpeg") {
		t.Fatalf("expected sanitized failure names, got %s", rr.Body.String())
	}
}

func containsReadinessValue(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
