package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"coding-plan-usage/internal/model"
)

func TestStoreSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	store := NewStore(path)
	value := New()
	value.LastDailyDate = "2026-08-24"
	value.Alerts["key"] = AlertRecord{Account: "a", Level: model.LevelWeekly, ResetTimestamp: 123, NotifiedAt: time.Unix(1, 0)}
	if err := store.Save(value); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.LastDailyDate != value.LastDailyDate || loaded.Alerts["key"].Account != "a" {
		t.Fatalf("loaded = %+v", loaded)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
}

func TestAlertDedupByResetWindowAndNoResetRearm(t *testing.T) {
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	value := New()
	usage := model.AccountUsage{Account: "a", Periods: []model.Period{
		{Level: model.LevelWeekly, Percent: 91, ResetTimestamp: now.Add(24 * time.Hour).Unix()},
		{Level: model.LevelSession, Percent: 95, ResetTimestamp: -1},
	}}
	if got := len(NewHighPeriods(value, []model.AccountUsage{usage}, 90)); got != 2 {
		t.Fatalf("new alerts = %d, want 2", got)
	}
	MarkHighPeriods(&value, []model.AccountUsage{usage}, 90, now)
	if got := len(NewHighPeriods(value, []model.AccountUsage{usage}, 90)); got != 0 {
		t.Fatalf("new alerts after mark = %d", got)
	}

	usage.Periods[0].ResetTimestamp = now.Add(7 * 24 * time.Hour).Unix()
	usage.Periods[1].Percent = 20
	if !RearmAndPrune(&value, []model.AccountUsage{usage}, 90, now) {
		t.Fatal("expected no-reset rearm to change state")
	}
	alerts := NewHighPeriods(value, []model.AccountUsage{usage}, 90)
	if len(alerts) != 1 || alerts[0].Level != model.LevelWeekly {
		t.Fatalf("alerts = %+v", alerts)
	}

	usage.Periods[1].Percent = 95
	alerts = NewHighPeriods(value, []model.AccountUsage{usage}, 90)
	if len(alerts) != 2 {
		t.Fatalf("alerts after no-reset rearm = %+v", alerts)
	}
}
