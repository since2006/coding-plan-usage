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

	"coding-plan-usage/internal/config"
	statuspage "coding-plan-usage/internal/status"
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
	defer func() {
		closeContext, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := handler.Close(closeContext); err != nil {
			t.Error(err)
		}
	}()

	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, statuspage.Path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	if body := response.Body.String(); !strings.Contains(body, "alerts_today 1\n") || !strings.Contains(body, "active_queries_today 1\n") {
		t.Fatalf("status body = %q", body)
	}
}
