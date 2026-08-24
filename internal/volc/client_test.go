package volc

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"coding-plan-usage/internal/model"
)

func TestClientParsesUsageAndSignsRequest(t *testing.T) {
	fixedNow := time.Date(2026, 8, 24, 1, 2, 3, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.URL.Query().Get("Action"); got != defaultAction {
			t.Errorf("Action = %q", got)
		}
		if got := request.URL.Query().Get("Region"); got != defaultRegion {
			t.Errorf("Region = %q", got)
		}
		for _, header := range []string{"Authorization", "X-Date", "X-Content-Sha256", "Content-Type"} {
			if request.Header.Get(header) == "" {
				t.Errorf("missing header %s", header)
			}
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{
          "ResponseMetadata":{"RequestId":"req-1"},
          "Result":{
            "Status":"Running",
            "UpdateTimestamp":1787533323000,
            "QuotaUsage":[
              {"Level":"monthly","Percent":"92.25","ResetTimestamp":1788000000},
              {"Level":"session","Percent":12.5,"ResetTimestamp":-1},
              {"Level":"weekly","Percent":80,"ResetTimestamp":1787600000}
            ]
          }
        }`)
	}))
	defer server.Close()

	client := NewClient(WithEndpoint(server.URL+"/"), WithNow(func() time.Time { return fixedNow }))
	usage, err := client.GetCodingPlanUsage(context.Background(), "AK", "SK")
	if err != nil {
		t.Fatalf("GetCodingPlanUsage() error = %v", err)
	}
	if usage.Status != "Running" || usage.UpdatedAt == nil {
		t.Fatalf("usage = %+v", usage)
	}
	if len(usage.Periods) != 3 {
		t.Fatalf("period count = %d", len(usage.Periods))
	}
	if usage.Periods[0].Level != model.LevelSession || usage.Periods[0].ResetAt != nil {
		t.Fatalf("session = %+v", usage.Periods[0])
	}
	if usage.Periods[2].Level != model.LevelMonthly || usage.Periods[2].Percent != 92.25 {
		t.Fatalf("monthly = %+v", usage.Periods[2])
	}
}

func TestClientRetriesServerFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			http.Error(writer, "temporary", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(writer, `{"Result":{"QuotaUsage":[{"Level":"weekly","Percent":1,"ResetTimestamp":1787600000}]}}`)
	}))
	defer server.Close()

	client := NewClient(
		WithEndpoint(server.URL+"/"),
		WithSleeper(func(context.Context, time.Duration) error { return nil }),
	)
	if _, err := client.GetCodingPlanUsage(context.Background(), "AK", "SK"); err != nil {
		t.Fatalf("GetCodingPlanUsage() error = %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("calls = %d, want 3", got)
	}
}

func TestClientDoesNotRetryErrorEnvelope(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(writer, `{"ResponseMetadata":{"RequestId":"req","Error":{"Code":"AccessDenied","Message":"denied"}}}`)
	}))
	defer server.Close()

	client := NewClient(WithEndpoint(server.URL+"/"), WithSleeper(func(context.Context, time.Duration) error { return nil }))
	_, err := client.GetCodingPlanUsage(context.Background(), "AK", "SK")
	if err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Fatalf("error = %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}
}

func TestClientRejectsEmptyAndMalformedResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "empty quota", body: `{"Result":{"QuotaUsage":[]}}`, want: ErrNoSubscription.Error()},
		{name: "invalid json", body: `{`, want: "解析火山响应 JSON"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(writer, test.body)
			}))
			defer server.Close()
			client := NewClient(WithEndpoint(server.URL + "/"))
			_, err := client.GetCodingPlanUsage(context.Background(), "AK", "SK")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestTimestampToTimeSupportsSecondsAndMilliseconds(t *testing.T) {
	seconds := timestampToTime(1_700_000_000)
	millis := timestampToTime(1_700_000_000_123)
	if seconds == nil || seconds.Unix() != 1_700_000_000 {
		t.Fatalf("seconds = %v", seconds)
	}
	if millis == nil || millis.UnixMilli() != 1_700_000_000_123 {
		t.Fatalf("millis = %v", millis)
	}
	if timestampToTime(-1) != nil {
		t.Fatal("negative timestamp should be nil")
	}
}
