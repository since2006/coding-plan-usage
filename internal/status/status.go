package status

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

const Path = "/status"

// Snapshot is the process-local activity recorded for the current calendar day.
type Snapshot struct {
	StartedAt     time.Time
	CurrentTime   time.Time
	Date          string
	Alerts        uint64
	ActiveQueries uint64
}

// Counter keeps lightweight status-page metrics in memory. The daily counters
// are reset lazily when the configured timezone advances to a new date.
type Counter struct {
	mutex    sync.Mutex
	location *time.Location
	now      func() time.Time
	started  time.Time
	date     string
	alerts   uint64
	queries  uint64
}

func New(location *time.Location, now func() time.Time) *Counter {
	if location == nil {
		location = time.Local
	}
	if now == nil {
		now = time.Now
	}
	started := now().In(location)
	return &Counter{
		location: location,
		now:      now,
		started:  started,
		date:     started.In(location).Format(time.DateOnly),
	}
}

func (counter *Counter) RecordAlert() {
	counter.mutex.Lock()
	defer counter.mutex.Unlock()
	counter.rollover(counter.now())
	counter.alerts++
}

func (counter *Counter) RecordActiveQuery() {
	counter.mutex.Lock()
	defer counter.mutex.Unlock()
	counter.rollover(counter.now())
	counter.queries++
}

func (counter *Counter) Snapshot() Snapshot {
	counter.mutex.Lock()
	defer counter.mutex.Unlock()
	now := counter.now()
	counter.rollover(now)
	return Snapshot{
		StartedAt:     counter.started,
		CurrentTime:   now,
		Date:          counter.date,
		Alerts:        counter.alerts,
		ActiveQueries: counter.queries,
	}
}

func (counter *Counter) rollover(now time.Time) {
	date := now.In(counter.location).Format(time.DateOnly)
	if date == counter.date {
		return
	}
	counter.date = date
	counter.alerts = 0
	counter.queries = 0
}

func (counter *Counter) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	snapshot := counter.Snapshot()
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if request.Method == http.MethodHead {
		writer.WriteHeader(http.StatusOK)
		return
	}
	_, _ = fmt.Fprintf(
		writer,
		"Coding Plan Usage Status\nstarted_at %s\nuptime_seconds %d\ndate %s\nalerts_today %d\nactive_queries_today %d\n",
		snapshot.StartedAt.Format(time.RFC3339),
		max(0, int64(snapshot.CurrentTime.Sub(snapshot.StartedAt)/time.Second)),
		snapshot.Date,
		snapshot.Alerts,
		snapshot.ActiveQueries,
	)
}
