package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"coding-plan-usage/internal/collector"
	"coding-plan-usage/internal/config"
	"coding-plan-usage/internal/model"
	"coding-plan-usage/internal/report"
	persist "coding-plan-usage/internal/state"
	"coding-plan-usage/internal/volc"
	"coding-plan-usage/internal/wecom"
	"coding-plan-usage/internal/zhipu"
)

func TestEndToEndCollectAggregateAndPush(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 8, 24, 8, 30, 0, 0, location)
	var apiCalls atomic.Int32
	apiServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		apiCalls.Add(1)
		if request.URL.Query().Get("Action") != "GetCodingPlanUsage" || request.Header.Get("Authorization") == "" {
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		fmt.Fprint(writer, `{"Result":{"Status":"Running","QuotaUsage":[{"Level":"session","Percent":95,"ResetTimestamp":1787600000},{"Level":"weekly","Percent":10,"ResetTimestamp":1788000000},{"Level":"monthly","Percent":20,"ResetTimestamp":1790000000}]}}`)
	}))
	defer apiServer.Close()

	var pushed string
	webhookServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload struct {
			MessageType string `json:"msgtype"`
			MarkdownV2  struct {
				Content string `json:"content"`
			} `json:"markdown_v2"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.MessageType != "markdown_v2" {
			t.Fatalf("msgtype = %q", payload.MessageType)
		}
		pushed += payload.MarkdownV2.Content
		fmt.Fprint(writer, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer webhookServer.Close()

	volcClient := volc.NewClient(
		volc.WithEndpoint(apiServer.URL+"/"),
		volc.WithNow(func() time.Time { return now }),
	)
	usageCollector := collector.New([]config.Account{{Name: "account-a", AccessKeyID: "AK", SecretAccessKey: "SK"}}, volcClient)
	renderer := report.NewRenderer(location, 90)
	sender := wecom.New(webhookServer.URL)
	store := persist.NewStore(filepath.Join(t.TempDir(), "state.json"))
	runner, err := NewRunner(RunnerConfig{Location: location, Threshold: 90, DailyHour: 9}, usageCollector, sender, renderer, store, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := runner.Execute(context.Background(), ModePoll)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !outcome.Sent || outcome.Kind != report.KindAlert || apiCalls.Load() != 1 {
		t.Fatalf("outcome = %+v, apiCalls = %d", outcome, apiCalls.Load())
	}
	if !strings.Contains(pushed, "account-a") || !strings.Contains(pushed, "95.0%") {
		t.Fatalf("pushed message = %s", pushed)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Alerts) != 1 {
		t.Fatalf("persisted alerts = %+v", loaded.Alerts)
	}
}

func TestEndToEndCollectZhipuCreditQuotaAndPush(t *testing.T) {
	now := time.Date(2026, 8, 24, 8, 30, 0, 0, time.UTC)
	apiServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got, want := request.Header.Get("Authorization"), "zhipu-key"; got != want {
			http.Error(writer, "bad authorization", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(writer, `{"code":200,"success":true,"data":{"level":"pro","limits":[
          {"type":"CREDIT_LIMIT","unit":3,"number":5,"percentage":95,"nextResetTime":1787600000000},
          {"type":"CREDIT_LIMIT","unit":6,"number":1,"percentage":10,"nextResetTime":1788000000000},
          {"type":"TIME_LIMIT","unit":5,"number":1,"usage":1000,"currentValue":200,"remaining":800,"percentage":20}
        ]}}`)
	}))
	defer apiServer.Close()

	var pushed string
	webhookServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload struct {
			MarkdownV2 struct {
				Content string `json:"content"`
			} `json:"markdown_v2"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		pushed += payload.MarkdownV2.Content
		fmt.Fprint(writer, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer webhookServer.Close()

	zhipuClient := zhipu.NewClient(zhipu.WithEndpoint(apiServer.URL), zhipu.WithMaxAttempts(1))
	usageCollector := collector.New(
		[]config.Account{{Name: "zhipu-account", Provider: config.ProviderZhipu, APIKey: "zhipu-key"}},
		nil,
		collector.WithZhipuClient(zhipuClient),
	)
	store := persist.NewStore(filepath.Join(t.TempDir(), "state.json"))
	runner, err := NewRunner(
		RunnerConfig{Location: time.UTC, Threshold: 90, DailyHour: 9},
		usageCollector,
		wecom.New(webhookServer.URL),
		report.NewRenderer(time.UTC, 90),
		store,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := runner.Execute(context.Background(), ModePoll)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !outcome.Sent || outcome.Kind != report.KindAlert {
		t.Fatalf("outcome = %+v", outcome)
	}
	if !strings.Contains(pushed, "zhipu-account") || !strings.Contains(pushed, "95.0%") || !strings.Contains(pushed, "20.0%") {
		t.Fatalf("pushed message = %s", pushed)
	}
	if _, exists := outcome.Usages[0].Period(model.LevelMonthly); exists {
		t.Fatalf("MCP quota was reported as model monthly: %+v", outcome.Usages[0].Periods)
	}
	if period, exists := outcome.Usages[0].Period(model.LevelMCPMonthly); !exists || period.Percent != 20 {
		t.Fatalf("MCP monthly period = %+v, exists = %v", period, exists)
	}
}
