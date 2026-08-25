package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"coding-plan-usage/internal/model"
	"coding-plan-usage/internal/report"
	persist "coding-plan-usage/internal/state"
)

type fakeCollector struct {
	usages []model.AccountUsage
	calls  int
}

func (collector *fakeCollector) Collect(context.Context) []model.AccountUsage {
	collector.calls++
	return append([]model.AccountUsage(nil), collector.usages...)
}

type fakeSender struct {
	calls    int
	messages [][]string
	err      error
}

func (sender *fakeSender) Send(_ context.Context, messages []string) error {
	sender.calls++
	sender.messages = append(sender.messages, append([]string(nil), messages...))
	return sender.err
}

type memoryStore struct {
	value persist.State
	saves int
	err   error
}

func (store *memoryStore) Load() (persist.State, error) { return store.value, nil }
func (store *memoryStore) Save(value persist.State) error {
	if store.err != nil {
		return store.err
	}
	store.value = value
	store.saves++
	return nil
}

func TestRunnerThresholdAlertDeduplicatesUntilNewResetWindow(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 8, 24, 8, 0, 0, 0, location)
	reset := now.Add(24 * time.Hour)
	collector := &fakeCollector{usages: []model.AccountUsage{{
		Account: "high",
		Periods: []model.Period{{Level: model.LevelWeekly, Percent: 91, ResetTimestamp: reset.Unix(), ResetAt: &reset}},
	}}}
	sender := &fakeSender{}
	store := &memoryStore{value: persist.New()}
	runner := newTestRunner(t, location, &now, collector, sender, store)

	outcome, err := runner.Execute(context.Background(), ModePoll)
	if err != nil || !outcome.Sent || outcome.Kind != report.KindAlert || sender.calls != 1 {
		t.Fatalf("first outcome = %+v, calls = %d, err = %v", outcome, sender.calls, err)
	}
	outcome, err = runner.Execute(context.Background(), ModePoll)
	if err != nil || outcome.Sent || sender.calls != 1 {
		t.Fatalf("second outcome = %+v, calls = %d, err = %v", outcome, sender.calls, err)
	}

	newReset := reset.Add(7 * 24 * time.Hour)
	collector.usages[0].Periods[0].ResetTimestamp = newReset.Unix()
	collector.usages[0].Periods[0].ResetAt = &newReset
	outcome, err = runner.Execute(context.Background(), ModePoll)
	if err != nil || !outcome.Sent || sender.calls != 2 {
		t.Fatalf("new window outcome = %+v, calls = %d, err = %v", outcome, sender.calls, err)
	}
}

func TestRunnerSeparatesOneHighAccountFromAllAccountStatistics(t *testing.T) {
	now := time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)
	sessionReset := now.Add(4 * time.Hour)
	weeklyReset := now.Add(6 * 24 * time.Hour)
	monthlyReset := now.Add(15 * 24 * time.Hour)
	collector := &fakeCollector{usages: []model.AccountUsage{
		{
			Account: "high",
			Periods: []model.Period{
				{Level: model.LevelSession, Percent: 91, ResetTimestamp: sessionReset.Unix(), ResetAt: &sessionReset},
				{Level: model.LevelWeekly, Percent: 92, ResetTimestamp: weeklyReset.Unix(), ResetAt: &weeklyReset},
				{Level: model.LevelMonthly, Percent: 93, ResetTimestamp: monthlyReset.Unix(), ResetAt: &monthlyReset},
			},
		},
		{Account: "normal"},
	}}
	sender := &fakeSender{}
	runner := newTestRunner(t, time.UTC, &now, collector, sender, &memoryStore{value: persist.New()})

	outcome, err := runner.Execute(context.Background(), ModePoll)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Sent || outcome.Kind != report.KindAlert || sender.calls != 1 || len(sender.messages[0]) != 1 {
		t.Fatalf("outcome = %+v, sender calls = %d, messages = %d", outcome, sender.calls, len(sender.messages[0]))
	}
	message := sender.messages[0][0]
	if !strings.Contains(message, "## 当前检测到的高用量账号（1 个）") || !strings.Contains(message, "## 全部账号用量统计") {
		t.Fatalf("alert sections are missing:\n%s", message)
	}
	if strings.Count(message, "- **high**：") != 2 || strings.Count(message, "normal") != 1 || strings.Contains(message, "| 账号 |") || strings.Contains(message, "⚠️") {
		t.Fatalf("alert must show the triggering account once in each section and all other accounts once:\n%s", message)
	}
	if strings.Count(message, "\n  - 重置：5 小时（04时00分钟），周（6天00时00分钟），月（15天00时00分钟）") != 2 {
		t.Fatalf("high-period reset times must be grouped on a separate line:\n%s", message)
	}
}

func TestRunnerDailyCatchupAndManualOnceSemantics(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 8, 24, 9, 5, 0, 0, location)
	collector := &fakeCollector{usages: []model.AccountUsage{{Account: "normal"}}}
	sender := &fakeSender{}
	store := &memoryStore{value: persist.New()}
	runner := newTestRunner(t, location, &now, collector, sender, store)

	outcome, err := runner.Execute(context.Background(), ModePoll)
	if err != nil || !outcome.Sent || outcome.Kind != report.KindDaily {
		t.Fatalf("daily outcome = %+v, err = %v", outcome, err)
	}
	if got := store.value.LastDailySlot; got != "2026-08-24T09:00" {
		t.Fatalf("LastDailySlot = %q", got)
	}
	outcome, err = runner.Execute(context.Background(), ModePoll)
	if err != nil || outcome.Sent {
		t.Fatalf("duplicate daily outcome = %+v, err = %v", outcome, err)
	}

	now = now.Add(24 * time.Hour)
	outcome, err = runner.Execute(context.Background(), ModeOnce)
	if err != nil || !outcome.Sent || outcome.Kind != report.KindOnce {
		t.Fatalf("manual outcome = %+v, err = %v", outcome, err)
	}
	if got := store.value.LastDailySlot; got != "2026-08-24T09:00" {
		t.Fatalf("manual once changed LastDailySlot to %q", got)
	}
}

func TestRunnerSendsEachConfiguredDailySlotOnce(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 8, 24, 8, 59, 0, 0, location)
	collector := &fakeCollector{usages: []model.AccountUsage{{Account: "normal"}}}
	sender := &fakeSender{}
	store := &memoryStore{value: persist.New()}
	renderer := report.NewRenderer(location, 90)
	runner, err := NewRunner(RunnerConfig{
		Location:   location,
		Threshold:  90,
		DailyTimes: []DailyTime{{Hour: 18}, {Hour: 9}},
	}, collector, sender, renderer, store, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	if outcome, executeErr := runner.Execute(context.Background(), ModePoll); executeErr != nil || outcome.Sent {
		t.Fatalf("before first slot outcome = %+v, err = %v", outcome, executeErr)
	}
	now = time.Date(2026, 8, 24, 9, 5, 0, 0, location)
	if outcome, executeErr := runner.Execute(context.Background(), ModePoll); executeErr != nil || !outcome.Sent || outcome.Kind != report.KindDaily {
		t.Fatalf("morning outcome = %+v, err = %v", outcome, executeErr)
	}
	if got := store.value.LastDailySlot; got != "2026-08-24T09:00" {
		t.Fatalf("morning LastDailySlot = %q", got)
	}
	if outcome, executeErr := runner.Execute(context.Background(), ModePoll); executeErr != nil || outcome.Sent {
		t.Fatalf("duplicate morning outcome = %+v, err = %v", outcome, executeErr)
	}

	now = time.Date(2026, 8, 24, 18, 5, 0, 0, location)
	if outcome, executeErr := runner.Execute(context.Background(), ModePoll); executeErr != nil || !outcome.Sent || outcome.Kind != report.KindDaily {
		t.Fatalf("evening outcome = %+v, err = %v", outcome, executeErr)
	}
	if got := store.value.LastDailySlot; got != "2026-08-24T18:00" {
		t.Fatalf("evening LastDailySlot = %q", got)
	}
	if sender.calls != 2 {
		t.Fatalf("sender calls = %d, want 2", sender.calls)
	}
}

func TestRunnerDoesNotPersistAlertWhenWebhookFails(t *testing.T) {
	location := time.UTC
	now := time.Date(2026, 8, 24, 8, 0, 0, 0, location)
	collector := &fakeCollector{usages: []model.AccountUsage{{
		Account: "high",
		Periods: []model.Period{{Level: model.LevelSession, Percent: 99, ResetTimestamp: 123}},
	}}}
	sender := &fakeSender{err: errors.New("webhook failed")}
	store := &memoryStore{value: persist.New()}
	runner := newTestRunner(t, location, &now, collector, sender, store)
	if _, err := runner.Execute(context.Background(), ModePoll); err == nil {
		t.Fatal("expected webhook error")
	}
	if len(store.value.Alerts) != 0 || store.saves != 0 {
		t.Fatalf("state persisted after failure: %+v, saves=%d", store.value, store.saves)
	}
}

func TestRunnerDryRunDoesNotSendOrSave(t *testing.T) {
	location := time.UTC
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, location)
	collector := &fakeCollector{usages: []model.AccountUsage{{Account: "a"}}}
	sender := &fakeSender{}
	store := &memoryStore{value: persist.New()}
	runner := newTestRunner(t, location, &now, collector, sender, store)
	outcome, err := runner.Execute(context.Background(), ModeDryRun)
	if err != nil || len(outcome.Messages) == 0 {
		t.Fatalf("outcome = %+v, err = %v", outcome, err)
	}
	if sender.calls != 0 || store.saves != 0 {
		t.Fatalf("sender calls = %d, saves = %d", sender.calls, store.saves)
	}
}

func TestDailyDue(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Shanghai")
	dailyTimes := []DailyTime{{Hour: 9}, {Hour: 18}}
	tests := []struct {
		name     string
		now      time.Time
		lastSlot string
		wantSlot string
		wantDue  bool
	}{
		{name: "before", now: time.Date(2026, 8, 24, 8, 59, 0, 0, location)},
		{name: "at morning", now: time.Date(2026, 8, 24, 9, 0, 0, 0, location), wantSlot: "2026-08-24T09:00", wantDue: true},
		{name: "catch up morning", now: time.Date(2026, 8, 24, 14, 0, 0, 0, location), wantSlot: "2026-08-24T09:00", wantDue: true},
		{name: "morning sent", now: time.Date(2026, 8, 24, 14, 0, 0, 0, location), lastSlot: "2026-08-24T09:00", wantSlot: "2026-08-24T09:00"},
		{name: "evening after morning", now: time.Date(2026, 8, 24, 18, 5, 0, 0, location), lastSlot: "2026-08-24T09:00", wantSlot: "2026-08-24T18:00", wantDue: true},
		{name: "late start only latest slot", now: time.Date(2026, 8, 24, 23, 0, 0, 0, location), wantSlot: "2026-08-24T18:00", wantDue: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotSlot, gotDue := DailyDue(test.now, location, dailyTimes, test.lastSlot)
			if gotSlot != test.wantSlot || gotDue != test.wantDue {
				t.Fatalf("DailyDue() = (%q, %v), want (%q, %v)", gotSlot, gotDue, test.wantSlot, test.wantDue)
			}
		})
	}
}

type concurrencyCollector struct {
	active    atomic.Int32
	maxActive atomic.Int32
	release   chan struct{}
}

func (collector *concurrencyCollector) Collect(context.Context) []model.AccountUsage {
	active := collector.active.Add(1)
	defer collector.active.Add(-1)
	for {
		current := collector.maxActive.Load()
		if active <= current || collector.maxActive.CompareAndSwap(current, active) {
			break
		}
	}
	<-collector.release
	return []model.AccountUsage{{Account: "a"}}
}

func TestRunnerSerializesConcurrentExecutions(t *testing.T) {
	now := time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)
	collector := &concurrencyCollector{release: make(chan struct{}, 2)}
	runner := newTestRunner(t, time.UTC, &now, collector, &fakeSender{}, &memoryStore{value: persist.New()})

	var waitGroup sync.WaitGroup
	waitGroup.Add(2)
	for index := 0; index < 2; index++ {
		go func() {
			defer waitGroup.Done()
			_, _ = runner.Execute(context.Background(), ModeDryRun)
		}()
	}
	for collector.active.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	collector.release <- struct{}{}
	for collector.active.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	collector.release <- struct{}{}
	waitGroup.Wait()
	if got := collector.maxActive.Load(); got != 1 {
		t.Fatalf("max concurrent executions = %d, want 1", got)
	}
}

func newTestRunner(t *testing.T, location *time.Location, now *time.Time, collector Collector, sender Sender, store StateStore) *Runner {
	t.Helper()
	renderer := report.NewRenderer(location, 90)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := NewRunner(RunnerConfig{
		Location:   location,
		Threshold:  90,
		DailyTimes: []DailyTime{{Hour: 9}},
	}, collector, sender, renderer, store, logger, func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	return runner
}
