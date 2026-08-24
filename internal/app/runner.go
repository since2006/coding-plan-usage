package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"coding-plan-usage/internal/model"
	"coding-plan-usage/internal/report"
	persist "coding-plan-usage/internal/state"
)

type Collector interface {
	Collect(ctx context.Context) []model.AccountUsage
}

type Sender interface {
	Send(ctx context.Context, messages []string) error
}

type StateStore interface {
	Load() (persist.State, error)
	Save(value persist.State) error
}

type Runner struct {
	collector   Collector
	sender      Sender
	renderer    *report.Renderer
	store       StateStore
	state       persist.State
	logger      *slog.Logger
	location    *time.Location
	threshold   float64
	dailyHour   int
	dailyMinute int
	now         func() time.Time

	mutex sync.Mutex
}

type RunnerConfig struct {
	Location    *time.Location
	Threshold   float64
	DailyHour   int
	DailyMinute int
}

type ExecuteMode string

const (
	ModePoll   ExecuteMode = "poll"
	ModeOnce   ExecuteMode = "once"
	ModeDryRun ExecuteMode = "dry-run"
)

type Outcome struct {
	Sent       bool
	Kind       report.Kind
	Messages   []string
	Usages     []model.AccountUsage
	DailyDue   bool
	NewAlerts  int
	Failed     int
	Successful int
}

func NewRunner(
	configuration RunnerConfig,
	collector Collector,
	sender Sender,
	renderer *report.Renderer,
	store StateStore,
	logger *slog.Logger,
	now func() time.Time,
) (*Runner, error) {
	value, err := store.Load()
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	if now == nil {
		now = time.Now
	}
	return &Runner{
		collector:   collector,
		sender:      sender,
		renderer:    renderer,
		store:       store,
		state:       value,
		logger:      logger,
		location:    configuration.Location,
		threshold:   configuration.Threshold,
		dailyHour:   configuration.DailyHour,
		dailyMinute: configuration.DailyMinute,
		now:         now,
	}, nil
}

func (runner *Runner) Execute(ctx context.Context, mode ExecuteMode) (Outcome, error) {
	runner.mutex.Lock()
	defer runner.mutex.Unlock()

	now := runner.now()
	usages := runner.collector.Collect(ctx)
	outcome := Outcome{Usages: usages}
	for _, usage := range usages {
		if usage.Error != "" {
			outcome.Failed++
			runner.logger.Error("账号用量查询失败", "account", usage.Account, "error", usage.Error)
		} else {
			outcome.Successful++
		}
	}

	if mode == ModeDryRun {
		outcome.Kind = report.KindOnce
		outcome.Messages = runner.renderer.Render(report.KindOnce, usages, now)
		return outcome, nil
	}

	stateChanged := persist.RearmAndPrune(&runner.state, usages, runner.threshold, now)
	newAlerts := persist.NewHighPeriods(runner.state, usages, runner.threshold)
	outcome.NewAlerts = len(newAlerts)
	outcome.DailyDue = DailyDue(now, runner.location, runner.dailyHour, runner.dailyMinute, runner.state.LastDailyDate)

	if mode == ModePoll && !outcome.DailyDue && len(newAlerts) == 0 {
		if stateChanged {
			if err := runner.store.Save(runner.state); err != nil {
				return outcome, err
			}
		}
		return outcome, nil
	}

	kind := report.KindOnce
	if mode == ModePoll {
		if outcome.DailyDue {
			kind = report.KindDaily
		} else {
			kind = report.KindAlert
		}
	}
	outcome.Kind = kind
	outcome.Messages = runner.renderer.Render(kind, usages, now)
	if err := runner.sender.Send(ctx, outcome.Messages); err != nil {
		return outcome, err
	}
	outcome.Sent = true

	persist.MarkHighPeriods(&runner.state, usages, runner.threshold, now)
	if kind == report.KindDaily {
		runner.state.LastDailyDate = now.In(runner.location).Format("2006-01-02")
	}
	if err := runner.store.Save(runner.state); err != nil {
		return outcome, fmt.Errorf("消息已发送，但保存去重状态失败: %w", err)
	}
	return outcome, nil
}

func (runner *Runner) Run(ctx context.Context, pollInterval time.Duration) error {
	runner.runPoll(ctx)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			runner.runPoll(ctx)
		}
	}
}

func (runner *Runner) runPoll(ctx context.Context) {
	startedAt := runner.now()
	outcome, err := runner.Execute(ctx, ModePoll)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			runner.logger.Error("用量检查失败", "error", err, "duration", runner.now().Sub(startedAt))
		}
		return
	}
	runner.logger.Info(
		"用量检查完成",
		"successful", outcome.Successful,
		"failed", outcome.Failed,
		"new_alerts", outcome.NewAlerts,
		"sent", outcome.Sent,
		"kind", outcome.Kind,
		"duration", runner.now().Sub(startedAt),
	)
}

func DailyDue(now time.Time, location *time.Location, hour, minute int, lastDailyDate string) bool {
	localNow := now.In(location)
	today := localNow.Format("2006-01-02")
	if lastDailyDate == today {
		return false
	}
	scheduled := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), hour, minute, 0, 0, location)
	return !localNow.Before(scheduled)
}
