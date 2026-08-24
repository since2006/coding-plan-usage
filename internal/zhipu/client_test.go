package zhipu

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

func TestClientParsesTokenWindowsAndIgnoresTimeLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Errorf("method = %s", request.Method)
		}
		if got, want := request.Header.Get("Authorization"), "zhipu-key"; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		fmt.Fprint(writer, `{
          "code":200,
          "success":true,
          "data":{
            "level":"pro",
            "limits":[
              {"type":"TIME_LIMIT","unit":5,"number":1,"usage":1000,"currentValue":250,"remaining":750,"percentage":"25"},
              {"type":"TOKENS_LIMIT","unit":6,"number":1,"percentage":44,"nextResetTime":1788000000000},
              {"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":"12.5","nextResetTime":1787600000123}
            ]
          }
        }`)
	}))
	defer server.Close()

	client := NewClient(WithEndpoint(server.URL), WithMaxAttempts(1))
	usage, err := client.GetCodingPlanUsage(context.Background(), "zhipu-key")
	if err != nil {
		t.Fatalf("GetCodingPlanUsage() error = %v", err)
	}
	if usage.Status != "pro" {
		t.Fatalf("status = %q", usage.Status)
	}
	if len(usage.Periods) != 2 {
		t.Fatalf("periods = %+v", usage.Periods)
	}
	assertPeriod(t, usage.Periods[0], model.LevelSession, 12.5, 1_787_600_000)
	assertPeriod(t, usage.Periods[1], model.LevelWeekly, 44, 1_788_000_000)
}

func TestClientParsesCreditLimitsAndDerivesPercentage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(writer, `{
          "code":"200",
          "success":true,
          "data":[
            {"type":"CREDIT_LIMIT","unit":"3","number":"5","usage":"2000","currentValue":"500","nextResetTime":"1787600000"},
            {"type":"CREDIT_LIMIT","unit":6,"number":1,"currentValue":25,"remaining":75,"nextResetTime":1788000000000}
          ]
        }`)
	}))
	defer server.Close()

	usage, err := NewClient(WithEndpoint(server.URL), WithMaxAttempts(1)).GetCodingPlanUsage(context.Background(), "key")
	if err != nil {
		t.Fatalf("GetCodingPlanUsage() error = %v", err)
	}
	assertPeriod(t, usage.Periods[0], model.LevelSession, 25, 1_787_600_000)
	assertPeriod(t, usage.Periods[1], model.LevelWeekly, 25, 1_788_000_000)
}

func TestClientFallsBackForLegacyUnclassifiedTokenLimits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(writer, `{"success":true,"data":{"limits":[
          {"type":"TOKENS_LIMIT","percentage":10},
          {"type":"TOKENS_LIMIT","percentage":20}
        ]}}`)
	}))
	defer server.Close()

	usage, err := NewClient(WithEndpoint(server.URL), WithMaxAttempts(1)).GetCodingPlanUsage(context.Background(), "key")
	if err != nil {
		t.Fatal(err)
	}
	assertPeriod(t, usage.Periods[0], model.LevelSession, 10, 0)
	assertPeriod(t, usage.Periods[1], model.LevelWeekly, 20, 0)
}

func TestClientRetriesTemporaryFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			http.Error(writer, "temporary", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(writer, `{"success":true,"data":{"limits":[{"type":"CREDIT_LIMIT","unit":3,"number":5,"percentage":1}]}}`)
	}))
	defer server.Close()

	client := NewClient(
		WithEndpoint(server.URL),
		WithSleeper(func(context.Context, time.Duration) error { return nil }),
	)
	if _, err := client.GetCodingPlanUsage(context.Background(), "key"); err != nil {
		t.Fatalf("GetCodingPlanUsage() error = %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("calls = %d, want 3", got)
	}
}

func TestClientRejectsAPIErrorAndEmptySubscription(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "api error", body: `{"code":401,"success":false,"msg":"token expired"}`, want: "token expired"},
		{name: "empty", body: `{"code":200,"success":true,"data":{"limits":[]}}`, want: ErrNoSubscription.Error()},
		{name: "invalid json", body: `{`, want: "解析智谱响应 JSON"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(writer, test.body)
			}))
			defer server.Close()
			_, err := NewClient(WithEndpoint(server.URL), WithMaxAttempts(1)).GetCodingPlanUsage(context.Background(), "key")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func assertPeriod(t *testing.T, period model.Period, level string, percent float64, resetTimestamp int64) {
	t.Helper()
	if period.Level != level || period.Percent != percent || period.ResetTimestamp != resetTimestamp {
		t.Fatalf("period = %+v, want level=%s percent=%v reset=%d", period, level, percent, resetTimestamp)
	}
	if resetTimestamp == 0 && period.ResetAt != nil {
		t.Fatalf("ResetAt = %v, want nil", period.ResetAt)
	}
	if resetTimestamp > 0 && (period.ResetAt == nil || period.ResetAt.Unix() != resetTimestamp) {
		t.Fatalf("ResetAt = %v, want unix %d", period.ResetAt, resetTimestamp)
	}
}

func findPeriod(periods []model.Period, level string) (model.Period, bool) {
	for _, period := range periods {
		if period.Level == level {
			return period, true
		}
	}
	return model.Period{}, false
}
