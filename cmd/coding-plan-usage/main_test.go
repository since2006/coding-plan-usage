package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"coding-plan-usage/internal/app"
	"coding-plan-usage/internal/config"
	"coding-plan-usage/internal/model"
	"coding-plan-usage/internal/report"
	persist "coding-plan-usage/internal/state"
	statuspage "coding-plan-usage/internal/status"
	usagepage "coding-plan-usage/internal/usage"
)

func TestWeComBotServerExposesStatusPage(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	counter := statuspage.New(time.UTC, func() time.Time { return now })
	counter.RecordAlert()
	counter.RecordActiveQuery()
	runtime := &applicationRuntime{
		config: config.Config{WeCom: config.WeCom{Bot: config.WeComBot{
			ListenAddress:  ":8080",
			Token:          "BotToken123",
			EncodingAESKey: "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG",
		}}},
		status: counter,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	handler, server, err := runtime.newWeComBotServer()
	if err != nil {
		t.Fatal(err)
	}
	defer closeBotHandler(t, handler)

	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, statuspage.Path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	if body := response.Body.String(); !strings.Contains(body, "alerts_today 1\n") || !strings.Contains(body, "active_queries_today 1\n") {
		t.Fatalf("status body = %q", body)
	}
}

func TestWeComBotServerExposesUsagePage(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	counter := statuspage.New(time.UTC, func() time.Time { return now })
	runner, err := app.NewRunner(
		app.RunnerConfig{Location: time.UTC, Threshold: 90, DailyTimes: []app.DailyTime{{Hour: 9}}},
		staticCollector{usages: []model.AccountUsage{{
			Account: "account-a",
			Periods: []model.Period{{Level: model.LevelSession, Percent: 25}},
		}}},
		unusedSender{},
		report.NewRenderer(time.UTC, 90),
		&runtimeMemoryStore{value: persist.New()},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &applicationRuntime{
		config: config.Config{WeCom: config.WeCom{Bot: config.WeComBot{
			ListenAddress:  ":8080",
			Token:          "BotToken123",
			EncodingAESKey: "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG",
		}}},
		runner: runner,
		status: counter,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	handler, server, err := runtime.newWeComBotServer()
	if err != nil {
		t.Fatal(err)
	}
	defer closeBotHandler(t, handler)

	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, usagepage.Path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if body := response.Body.String(); !strings.Contains(body, "Coding Plan 用量汇总") || !strings.Contains(body, "account-a") || !strings.Contains(body, "25.0%") {
		t.Fatalf("usage body = %q", body)
	}
	if got := counter.Snapshot().ActiveQueries; got != 1 {
		t.Fatalf("active queries = %d, want 1", got)
	}
}

type staticCollector struct {
	usages []model.AccountUsage
}

func (collector staticCollector) Collect(context.Context) []model.AccountUsage {
	return append([]model.AccountUsage(nil), collector.usages...)
}

type unusedSender struct{}

func (unusedSender) Send(context.Context, []string) error { return nil }

type runtimeMemoryStore struct {
	value persist.State
}

func (store *runtimeMemoryStore) Load() (persist.State, error) { return store.value, nil }
func (store *runtimeMemoryStore) Save(value persist.State) error {
	store.value = value
	return nil
}

func closeBotHandler(t *testing.T, handler interface {
	Close(context.Context) error
}) {
	t.Helper()
	closeContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := handler.Close(closeContext); err != nil {
		t.Error(err)
	}
}
