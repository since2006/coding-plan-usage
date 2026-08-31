package usage

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandlerReturnsUsageSummary(t *testing.T) {
	queryCalls := 0
	handler, err := NewHandler(func(ctx context.Context) (string, error) {
		queryCalls++
		if ctx == nil {
			t.Fatal("query context is nil")
		}
		return "# Coding Plan 用量汇总\n- **account-a**：5 小时 25.0%", nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, Path, nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "text/markdown; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got, want := response.Body.String(), "# Coding Plan 用量汇总\n- **account-a**：5 小时 25.0%\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	if queryCalls != 1 {
		t.Fatalf("query calls = %d, want 1", queryCalls)
	}
}

func TestHandlerRejectsUnsupportedMethodsWithoutQuerying(t *testing.T) {
	queryCalls := 0
	handler, err := NewHandler(func(context.Context) (string, error) {
		queryCalls++
		return "unused", nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, Path, nil))

	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("status = %d, Allow = %q", response.Code, response.Header().Get("Allow"))
	}
	if queryCalls != 0 {
		t.Fatalf("query calls = %d, want 0", queryCalls)
	}
}

func TestHandlerReturnsBadGatewayWithoutLeakingQueryError(t *testing.T) {
	handler, err := NewHandler(func(context.Context) (string, error) {
		return "", errors.New("upstream secret details")
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, Path, nil))

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d", response.Code)
	}
	if got, want := response.Body.String(), "failed to query usage\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestNewHandlerRejectsNilQuery(t *testing.T) {
	if _, err := NewHandler(nil, nil); err == nil {
		t.Fatal("expected nil query error")
	}
}
