package usage

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerReturnsHTMLUsagePageByDefault(t *testing.T) {
	queryCalls := 0
	handler, err := NewHandler(func(ctx context.Context) (string, error) {
		queryCalls++
		if ctx == nil {
			t.Fatal("query context is nil")
		}
		return "# Coding Plan 用量汇总\n> 统计时间：2026-09-01 12:00:00 CST\n\n- **account-a**：5 小时 25.0%", nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, Path, nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := response.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'none'") {
		t.Fatalf("Content-Security-Policy = %q", got)
	}
	body := response.Body.String()
	for _, want := range []string{
		"<!doctype html>",
		"<h1>Coding Plan 用量汇总</h1>",
		"<blockquote>",
		"<strong>account-a</strong>",
		`href="/usage?format=markdown"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body does not contain %q:\n%s", want, body)
		}
	}
	if queryCalls != 1 {
		t.Fatalf("query calls = %d, want 1", queryCalls)
	}
}

func TestHandlerReturnsRawMarkdownOnRequest(t *testing.T) {
	handler, err := NewHandler(func(context.Context) (string, error) {
		return "# Coding Plan 用量汇总\n- **account-a**：5 小时 25.0%", nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, Path+"?format=markdown", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "text/markdown; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got, want := response.Body.String(), "# Coding Plan 用量汇总\n- **account-a**：5 小时 25.0%\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestHandlerDoesNotRenderUnsafeMarkdown(t *testing.T) {
	handler, err := NewHandler(func(context.Context) (string, error) {
		return "# Safe\n<script>alert(1)</script>\n[unsafe](javascript:alert(1))", nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, Path, nil))

	body := strings.ToLower(response.Body.String())
	if strings.Contains(body, "<script") || strings.Contains(body, "javascript:") {
		t.Fatalf("unsafe markdown was rendered:\n%s", response.Body.String())
	}
}

func TestHandlerRejectsUnsupportedMethodAndFormatWithoutQuerying(t *testing.T) {
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
		t.Fatalf("POST status = %d, Allow = %q", response.Code, response.Header().Get("Allow"))
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, Path+"?format=json", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid format status = %d", response.Code)
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
