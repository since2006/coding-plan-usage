package status

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCounterRecordsAndResetsAtLocalMidnight(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 28, 23, 59, 0, 0, location)
	counter := New(location, func() time.Time { return now })
	counter.RecordAlert()
	counter.RecordActiveQuery()
	counter.RecordActiveQuery()

	snapshot := counter.Snapshot()
	if snapshot.Date != "2026-08-28" || snapshot.Alerts != 1 || snapshot.ActiveQueries != 2 {
		t.Fatalf("snapshot = %+v", snapshot)
	}

	now = now.Add(2 * time.Minute)
	snapshot = counter.Snapshot()
	if snapshot.Date != "2026-08-29" || snapshot.Alerts != 0 || snapshot.ActiveQueries != 0 {
		t.Fatalf("snapshot after midnight = %+v", snapshot)
	}
}

func TestCounterSupportsConcurrentRecording(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	counter := New(time.UTC, func() time.Time { return now })
	var workers sync.WaitGroup
	for range 100 {
		workers.Add(2)
		go func() {
			defer workers.Done()
			counter.RecordAlert()
		}()
		go func() {
			defer workers.Done()
			counter.RecordActiveQuery()
		}()
	}
	workers.Wait()

	snapshot := counter.Snapshot()
	if snapshot.Alerts != 100 || snapshot.ActiveQueries != 100 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestStatusPage(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 5, 0, time.UTC)
	counter := New(time.UTC, func() time.Time { return now })
	counter.RecordAlert()
	counter.RecordActiveQuery()
	now = now.Add(5 * time.Second)

	response := httptest.NewRecorder()
	counter.ServeHTTP(response, httptest.NewRequest(http.MethodGet, Path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	wantLines := []string{
		"started_at 2026-08-28T12:00:05Z",
		"uptime_seconds 5",
		"date 2026-08-28",
		"alerts_today 1",
		"active_queries_today 1",
	}
	for _, line := range wantLines {
		if !strings.Contains(response.Body.String(), line+"\n") {
			t.Fatalf("body does not contain %q:\n%s", line, response.Body.String())
		}
	}

	response = httptest.NewRecorder()
	counter.ServeHTTP(response, httptest.NewRequest(http.MethodPost, Path, nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", response.Code)
	}
}
