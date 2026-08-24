package wecom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSendMarkdownV2Messages(t *testing.T) {
	var contents []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
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
			t.Errorf("msgtype = %q", payload.MessageType)
		}
		contents = append(contents, payload.MarkdownV2.Content)
		fmt.Fprint(writer, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer server.Close()

	client := New(server.URL)
	if err := client.Send(context.Background(), []string{"one", "two"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if len(contents) != 2 || contents[0] != "one" || contents[1] != "two" {
		t.Fatalf("contents = %v", contents)
	}
}

func TestSendRetriesTransientWeComError(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			fmt.Fprint(writer, `{"errcode":-1,"errmsg":"system busy"}`)
			return
		}
		fmt.Fprint(writer, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer server.Close()

	client := New(server.URL, WithSleeper(func(context.Context, time.Duration) error { return nil }))
	if err := client.Send(context.Background(), []string{"message"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("calls = %d, want 3", got)
	}
}

func TestSendDoesNotExposeWebhookURLInError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(writer, `{"errcode":40058,"errmsg":"invalid parameter"}`)
	}))
	defer server.Close()
	client := New(server.URL + "/secret-key")
	if err := client.Send(context.Background(), []string{"message"}); err == nil {
		t.Fatal("expected error")
	} else if strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("error leaked webhook URL: %v", err)
	}
}

func TestSendRedactsURLFromTransportError(t *testing.T) {
	secretURL := "https://example.invalid/webhook?key=super-secret"
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Post", URL: request.URL.String(), Err: errors.New("dial failed")}
	})}
	client := New(
		secretURL,
		WithHTTPClient(httpClient),
		WithMaxAttempts(1),
	)
	err := client.Send(context.Background(), []string{"message"})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "super-secret") || strings.Contains(err.Error(), secretURL) {
		t.Fatalf("error leaked webhook URL: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
