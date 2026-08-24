package report

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"coding-plan-usage/internal/model"
)

func TestRenderPreservesNonLimitOrderAndEscapesUserText(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Shanghai")
	renderer := NewRenderer(location, 90)
	generatedAt := time.Date(2026, 8, 24, 9, 0, 0, 0, location)
	nearReset := generatedAt.Add(30 * time.Minute)
	farReset := generatedAt.Add(24 * time.Hour)
	usages := []model.AccountUsage{
		{Account: "<high_*>", Periods: []model.Period{{Level: model.LevelSession, Percent: 90}, {Level: model.LevelMonthly, Percent: 90.1, ResetAt: &farReset}}},
		{Account: "normal", Periods: []model.Period{{Level: model.LevelWeekly, Percent: 20, ResetAt: &nearReset}}},
		{Account: "failed", Error: "AccessDenied <secret>"},
	}
	messages := renderer.Render(KindAlert, usages, generatedAt)
	if len(messages) != 1 {
		t.Fatalf("message count = %d", len(messages))
	}
	message := messages[0]
	highIndex := strings.Index(message, "&lt;high\\_\\*&gt;")
	normalIndex := strings.Index(message, "normal")
	failedIndex := strings.Index(message, "failed")
	if highIndex < 0 || normalIndex < highIndex || failedIndex < normalIndex {
		t.Fatalf("unexpected order or escaping:\n%s", message)
	}
	if !strings.Contains(message, `**90.1%**`) {
		t.Fatalf("high period not highlighted:\n%s", message)
	}
	if strings.Contains(message, `**90.0%**`) {
		t.Fatal("threshold must be strictly greater than 90")
	}
	if !strings.Contains(message, "- **&lt;high\\_\\*&gt;**：5 小时 90.0%，月 **90.1%**\n  - 重置：月（1天00时00分钟）") {
		t.Fatalf("compact account summary missing:\n%s", message)
	}
	if strings.Contains(message, "⚠️") || strings.Contains(message, "**1. &lt;high") {
		t.Fatalf("warning icon or account index should not be rendered:\n%s", message)
	}
	if strings.Contains(message, "周期 已用") {
		t.Fatalf("format explanation should not be rendered:\n%s", message)
	}
	if strings.Contains(message, "重置：5 小时") || strings.Contains(message, "重置：周") {
		t.Fatalf("reset time should be hidden at or below the configured threshold:\n%s", message)
	}
	if strings.Contains(message, "| 账号 |") || strings.Contains(message, "无数据") || strings.Contains(message, "暂无活跃窗口") {
		t.Fatalf("report should omit table markup and empty-period placeholders:\n%s", message)
	}
}

func TestRenderSortsReachedLimitByItsOwnReset(t *testing.T) {
	renderer := NewRenderer(time.UTC, 90)
	generatedAt := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	sessionSoon := generatedAt.Add(4 * time.Hour)
	twoDays := generatedAt.Add(2 * 24 * time.Hour)
	sixDays := generatedAt.Add(6 * 24 * time.Hour)
	fifteenDays := generatedAt.Add(15 * 24 * time.Hour)

	message := renderer.Render(KindOnce, []model.AccountUsage{
		{Account: "limit-fifteen-days-a", Periods: []model.Period{
			{Level: model.LevelSession, Percent: 0, ResetAt: &sessionSoon},
			{Level: model.LevelWeekly, Percent: 0, ResetAt: &sixDays},
			{Level: model.LevelMonthly, Percent: 100, ResetAt: &fifteenDays},
		}},
		{Account: "limit-fifteen-days-b", Periods: []model.Period{
			{Level: model.LevelSession, Percent: 0, ResetAt: &sessionSoon},
			{Level: model.LevelWeekly, Percent: 0, ResetAt: &sixDays},
			{Level: model.LevelMonthly, Percent: 100, ResetAt: &fifteenDays},
		}},
		{Account: "limit-two-days", Periods: []model.Period{
			{Level: model.LevelSession, Percent: 0},
			{Level: model.LevelWeekly, Percent: 0, ResetAt: &sixDays},
			{Level: model.LevelMonthly, Percent: 100, ResetAt: &twoDays},
		}},
		{Account: "below-limit", Periods: []model.Period{
			{Level: model.LevelSession, Percent: 0},
			{Level: model.LevelWeekly, Percent: 0, ResetAt: &sixDays},
			{Level: model.LevelMonthly, Percent: 98.8, ResetAt: &twoDays},
		}},
	}, generatedAt)[0]

	wantOrder := []string{"limit-two-days", "limit-fifteen-days-a", "limit-fifteen-days-b", "below-limit"}
	previous := -1
	for _, account := range wantOrder {
		index := strings.Index(message, account)
		if index <= previous {
			t.Fatalf("account %q is out of order:\n%s", account, message)
		}
		previous = index
	}
}

func TestRenderPutsReachedLimitBeforeNearerReset(t *testing.T) {
	renderer := NewRenderer(time.UTC, 90)
	generatedAt := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	nearReset := generatedAt.Add(30 * time.Minute)
	farReset := generatedAt.Add(15 * 24 * time.Hour)

	message := renderer.Render(KindOnce, []model.AccountUsage{
		{Account: "below-limit-near", Periods: []model.Period{{Level: model.LevelSession, Percent: 89.9, ResetAt: &nearReset}}},
		{Account: "reached-limit-far", Periods: []model.Period{{Level: model.LevelMonthly, Percent: 100, ResetAt: &farReset}}},
	}, generatedAt)[0]

	reachedIndex := strings.Index(message, "reached-limit-far")
	belowIndex := strings.Index(message, "below-limit-near")
	if reachedIndex < 0 || belowIndex < reachedIndex {
		t.Fatalf("account at 100%% must come first:\n%s", message)
	}
	if !strings.Contains(message, "月 **100.0%**\n  - 重置：月（15天00时00分钟）") || strings.Contains(message, "5 小时（30分钟）") {
		t.Fatalf("reset time must only be shown above the configured threshold:\n%s", message)
	}
}

func TestRenderPreservesCollectionOrderWhenReachedLimitResetIsEqualOrMissing(t *testing.T) {
	renderer := NewRenderer(time.UTC, 90)
	generatedAt := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	reset := generatedAt.Add(24 * time.Hour)

	message := renderer.Render(KindOnce, []model.AccountUsage{
		{Account: "z-equal", Periods: []model.Period{{Level: model.LevelSession, Percent: 100, ResetAt: &reset}}},
		{Account: "a-equal", Periods: []model.Period{{Level: model.LevelWeekly, Percent: 100, ResetAt: &reset}}},
		{Account: "z-missing", Error: "query failed"},
		{Account: "a-missing"},
	}, generatedAt)[0]

	wantOrder := []string{"z-equal", "a-equal", "z-missing", "a-missing"}
	previous := -1
	for _, account := range wantOrder {
		index := strings.Index(message, account)
		if index <= previous {
			t.Fatalf("account %q is out of order:\n%s", account, message)
		}
		previous = index
	}
}

func TestRenderSplitsLargeReportAtAccountBoundaries(t *testing.T) {
	renderer := NewRenderer(time.UTC, 90)
	usages := make([]model.AccountUsage, 0, 80)
	for index := 0; index < 80; index++ {
		usages = append(usages, model.AccountUsage{
			Account: fmt.Sprintf("account-%03d", index),
			Periods: []model.Period{
				{Level: model.LevelSession, Percent: float64(index)},
				{Level: model.LevelWeekly, Percent: 1},
				{Level: model.LevelMonthly, Percent: 2},
			},
		})
	}
	messages := renderer.Render(KindDaily, usages, time.Now())
	if len(messages) < 2 {
		t.Fatalf("message count = %d, want multiple", len(messages))
	}
	combined := strings.Join(messages, "\n")
	for _, usage := range usages {
		if got := strings.Count(combined, usage.Account); got != 1 {
			t.Fatalf("account %s count = %d", usage.Account, got)
		}
	}
	for _, message := range messages {
		if len(message) > weComMarkdownLimit {
			t.Fatalf("message length = %d", len(message))
		}
		if strings.Contains(message, "| 账号 |") {
			t.Fatalf("message unexpectedly contains a markdown table:\n%s", message)
		}
	}
}

func TestRenderEscapesMarkdownAndFlattensNewlines(t *testing.T) {
	renderer := NewRenderer(time.UTC, 90)
	message := renderer.Render(KindOnce, []model.AccountUsage{{
		Account: "account*_one\nnext",
		Error:   "failed_[reason]\nmore",
	}}, time.Now())[0]
	if !strings.Contains(message, `account\*\_one next`) || !strings.Contains(message, `failed\_\[reason\] more`) {
		t.Fatalf("inline markdown was not escaped:\n%s", message)
	}
}

func TestRenderIgnoresNonCanonicalPeriods(t *testing.T) {
	renderer := NewRenderer(time.UTC, 90)
	message := renderer.Render(KindOnce, []model.AccountUsage{{
		Account: "mixed",
		Periods: []model.Period{
			{Level: model.LevelMonthly, Percent: 12},
			{Level: "mcp_monthly", Percent: 34},
		},
	}}, time.Now())[0]
	if !strings.Contains(message, "- **mixed**：月 12.0%") || strings.Contains(message, "34.0%") || strings.Contains(message, "MCP") {
		t.Fatalf("non-canonical period was rendered:\n%s", message)
	}
}

func TestFormatRemainingTime(t *testing.T) {
	tests := []struct {
		name      string
		remaining time.Duration
		want      string
	}{
		{name: "days", remaining: 15*24*time.Hour + 8*time.Hour + 30*time.Minute, want: "15天08时30分钟"},
		{name: "hours", remaining: 8*time.Hour + 5*time.Minute, want: "08时05分钟"},
		{name: "minutes", remaining: 30 * time.Minute, want: "30分钟"},
		{name: "round seconds up", remaining: 10 * time.Second, want: "1分钟"},
		{name: "expired", remaining: -time.Second, want: "已重置"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := formatRemainingTime(test.remaining); got != test.want {
				t.Fatalf("formatRemainingTime() = %q, want %q", got, test.want)
			}
		})
	}
}
