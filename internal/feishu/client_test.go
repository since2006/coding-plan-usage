package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSendInteractiveCardWithSignature(t *testing.T) {
	fixedTime := time.Unix(1_700_000_000, 0)
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var payload struct {
			Timestamp   int64  `json:"timestamp"`
			Sign        string `json:"sign"`
			MessageType string `json:"msg_type"`
			Card        struct {
				Header struct {
					Title struct {
						Tag     string `json:"tag"`
						Content string `json:"content"`
					} `json:"title"`
				} `json:"header"`
				Elements []struct {
					Text struct {
						Tag     string `json:"tag"`
						Content string `json:"content"`
					} `json:"text"`
				} `json:"elements"`
			} `json:"card"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Timestamp != fixedTime.Unix() {
			t.Errorf("timestamp = %d", payload.Timestamp)
		}
		if payload.Sign != "fiWS2+gh28DOydAv7hzONH/mDn9+b1Y4Y5ivXWXy8vA=" {
			t.Errorf("sign = %q", payload.Sign)
		}
		if payload.MessageType != "interactive" {
			t.Errorf("msg_type = %q", payload.MessageType)
		}
		if payload.Card.Header.Title.Tag != "plain_text" || payload.Card.Header.Title.Content != "Coding Plan 用量日报" {
			t.Errorf("title = %+v", payload.Card.Header.Title)
		}
		if len(payload.Card.Elements) != 1 || payload.Card.Elements[0].Text.Tag != "lark_md" || payload.Card.Elements[0].Text.Content != "> 统计时间：now" {
			t.Errorf("elements = %+v", payload.Card.Elements)
		}
		return jsonResponse(http.StatusOK, `{"code":0,"msg":"success"}`), nil
	})}

	client := New(
		"https://example.invalid/hook",
		"secret",
		WithHTTPClient(httpClient),
		WithClock(func() time.Time { return fixedTime }),
	)
	if err := client.Send(context.Background(), []string{"# Coding Plan 用量日报\n> 统计时间：now"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
}

func TestSendAcceptsLegacySuccessResponse(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"Extra":null,"StatusCode":0,"StatusMessage":"success"}`), nil
	})}

	client := New("https://example.invalid/hook", "", WithHTTPClient(httpClient))
	if err := client.Send(context.Background(), []string{"message"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
}

func TestSendRetriesTransientError(t *testing.T) {
	var calls atomic.Int32
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) < 3 {
			return jsonResponse(http.StatusOK, `{"code":-1,"msg":"system busy"}`), nil
		}
		return jsonResponse(http.StatusOK, `{"code":0,"msg":"success"}`), nil
	})}

	client := New(
		"https://example.invalid/hook",
		"",
		WithHTTPClient(httpClient),
		WithSleeper(func(context.Context, time.Duration) error { return nil }),
	)
	if err := client.Send(context.Background(), []string{"message"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("calls = %d, want 3", got)
	}
}

func TestSendDoesNotExposeWebhookURLInErrors(t *testing.T) {
	t.Run("business error", func(t *testing.T) {
		httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, `{"code":19024,"msg":"Key Words Not Found"}`), nil
		})}
		client := New("https://example.invalid/hook/secret-hook", "", WithHTTPClient(httpClient))
		err := client.Send(context.Background(), []string{"message"})
		if err == nil {
			t.Fatal("expected error")
		}
		if strings.Contains(err.Error(), "secret-hook") {
			t.Fatalf("error leaked webhook URL: %v", err)
		}
	})

	t.Run("transport error", func(t *testing.T) {
		secretURL := "https://example.invalid/hook/super-secret"
		httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return nil, &url.Error{Op: "Post", URL: request.URL.String(), Err: errors.New("dial failed")}
		})}
		client := New(secretURL, "sign-secret", WithHTTPClient(httpClient), WithMaxAttempts(1))
		err := client.Send(context.Background(), []string{"message"})
		if err == nil {
			t.Fatal("expected error")
		}
		if strings.Contains(err.Error(), "super-secret") || strings.Contains(err.Error(), "sign-secret") {
			t.Fatalf("error leaked secret: %v", err)
		}
	})
}

func jsonResponse(statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
