package report

import (
	"fmt"
	"html"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"coding-plan-usage/internal/model"
)

const (
	weComMarkdownLimit = 4096
	chunkSafetyLimit   = 3900
)

var periodLabels = map[string]string{
	model.LevelSession: "5 小时",
	model.LevelWeekly:  "周",
	model.LevelMonthly: "月",
}

type Kind string

const (
	KindDaily Kind = "daily"
	KindAlert Kind = "alert"
	KindOnce  Kind = "once"
)

type Renderer struct {
	location  *time.Location
	threshold float64
}

type section struct {
	title   string
	entries []string
}

func NewRenderer(location *time.Location, threshold float64) *Renderer {
	return &Renderer{location: location, threshold: threshold}
}

func (renderer *Renderer) Render(kind Kind, usages []model.AccountUsage, generatedAt time.Time) []string {
	sorted := renderer.sortUsages(usages)
	return renderer.renderSections(kind, []section{{entries: renderer.accountEntries(sorted, generatedAt)}}, generatedAt)
}

// RenderAlert separates the accounts that triggered this notification from the
// complete usage report. Both sections retain the same ordering used by daily
// reports, and an account that triggered multiple periods is shown only once in
// the first section.
func (renderer *Renderer) RenderAlert(usages []model.AccountUsage, alertedAccounts []string, generatedAt time.Time) []string {
	sorted := renderer.sortUsages(usages)
	alerted := make(map[string]struct{}, len(alertedAccounts))
	for _, account := range alertedAccounts {
		alerted[account] = struct{}{}
	}

	highUsages := make([]model.AccountUsage, 0, len(alerted))
	seen := make(map[string]struct{}, len(alerted))
	for _, usage := range sorted {
		if _, exists := alerted[usage.Account]; !exists {
			continue
		}
		if _, exists := seen[usage.Account]; exists {
			continue
		}
		seen[usage.Account] = struct{}{}
		highUsages = append(highUsages, usage)
	}

	sections := []section{
		{
			title:   fmt.Sprintf("当前检测到的高用量账号（%d 个）", len(highUsages)),
			entries: renderer.accountEntries(highUsages, generatedAt),
		},
		{
			title:   "全部账号用量统计",
			entries: renderer.accountEntries(sorted, generatedAt),
		},
	}
	return renderer.renderSections(KindAlert, sections, generatedAt)
}

func (renderer *Renderer) accountEntries(usages []model.AccountUsage, generatedAt time.Time) []string {
	entries := make([]string, 0, len(usages))
	for _, usage := range usages {
		entries = append(entries, renderer.accountEntry(usage, generatedAt))
	}
	return entries
}

func (renderer *Renderer) renderSections(kind Kind, sections []section, generatedAt time.Time) []string {
	baseHeader := renderer.header(kind, generatedAt, "")
	groups := make([][]string, 0, 1)
	current := make([]string, 0)
	currentSize := len(baseHeader)
	flush := func() {
		groups = append(groups, current)
		current = make([]string, 0)
		currentSize = len(baseHeader)
	}
	appendBlock := func(block string) {
		current = append(current, block)
		currentSize += len(block) + 1
	}

	for _, reportSection := range sections {
		heading := ""
		if reportSection.title != "" {
			heading = "## " + reportSection.title + "\n"
			if currentSize+len(heading)+1 > chunkSafetyLimit && len(current) > 0 {
				flush()
			}
			appendBlock(heading)
		}

		entriesInChunk := 0
		for _, entry := range reportSection.entries {
			if currentSize+len(entry)+1 > chunkSafetyLimit && len(current) > 0 {
				switch {
				case heading != "" && entriesInChunk == 0:
					// Move a newly appended heading to the next chunk so it
					// stays together with the section's first account.
					current = current[:len(current)-1]
					currentSize -= len(heading) + 1
					if len(current) > 0 {
						flush()
					}
					appendBlock(heading)
				case heading != "":
					flush()
					appendBlock("## " + reportSection.title + "（续）\n")
					entriesInChunk = 0
				default:
					flush()
				}
			}
			appendBlock(entry)
			entriesInChunk++
		}
	}
	if len(current) > 0 || len(groups) == 0 {
		groups = append(groups, current)
	}

	messages := make([]string, 0, len(groups))
	for index, group := range groups {
		suffix := ""
		if len(groups) > 1 {
			suffix = fmt.Sprintf("（%d/%d）", index+1, len(groups))
		}
		message := renderer.header(kind, generatedAt, suffix) + strings.Join(group, "\n")
		if len(message) > weComMarkdownLimit {
			message = truncateUTF8(message, weComMarkdownLimit-32) + "\n> 内容过长，已截断"
		}
		messages = append(messages, message)
	}
	return messages
}

func (renderer *Renderer) header(kind Kind, generatedAt time.Time, suffix string) string {
	title := "Coding Plan 用量汇总"
	switch kind {
	case KindDaily:
		title = "Coding Plan 用量日报"
	case KindAlert:
		title = "Coding Plan 高用量提醒"
	}
	return fmt.Sprintf(
		"# %s%s\n> 统计时间：%s\n\n",
		title,
		suffix,
		generatedAt.In(renderer.location).Format("2006-01-02 15:04:05 MST"),
	)
}

func (renderer *Renderer) accountEntry(usage model.AccountUsage, generatedAt time.Time) string {
	name := escapeInlineText(truncateUTF8(usage.Account, 120))
	if usage.Error != "" {
		errorMessage := escapeInlineText(truncateUTF8(usage.Error, 240))
		return fmt.Sprintf("- ❌ **%s**：查询失败（%s）", name, errorMessage)
	}

	account := "**" + name + "**"
	periods := make([]string, 0, len(model.CanonicalLevels))
	resets := make([]string, 0, len(model.CanonicalLevels))
	for _, level := range model.CanonicalLevels {
		period, exists := usage.Period(level)
		if !exists {
			continue
		}
		percentage := fmt.Sprintf("%.1f%%", period.Percent)
		if period.Percent > renderer.threshold {
			percentage = "**" + percentage + "**"
		}
		label := periodLabels[level]
		periodSummary := label + " " + percentage
		if period.Percent > renderer.threshold && period.ResetAt != nil {
			resets = append(resets, label+"（"+formatRemainingTime(period.ResetAt.Sub(generatedAt))+"）")
		}
		periods = append(periods, periodSummary)
	}
	if len(periods) == 0 {
		return fmt.Sprintf("- %s：暂无用量数据", account)
	}
	entry := fmt.Sprintf("- %s：%s", account, strings.Join(periods, "，"))
	if len(resets) > 0 {
		entry += "\n  - 重置：" + strings.Join(resets, "，")
	}
	return entry
}

func formatRemainingTime(remaining time.Duration) string {
	if remaining <= 0 {
		return "已重置"
	}
	// 向上取整到分钟，避免尚有数秒时显示为 00 分钟。
	totalMinutes := int64((remaining + time.Minute - 1) / time.Minute)
	days := totalMinutes / (24 * 60)
	hours := totalMinutes % (24 * 60) / 60
	minutes := totalMinutes % 60

	switch {
	case days > 0:
		return fmt.Sprintf("%d天%02d时%02d分钟", days, hours, minutes)
	case hours > 0:
		return fmt.Sprintf("%02d时%02d分钟", hours, minutes)
	default:
		return fmt.Sprintf("%d分钟", minutes)
	}
}

func (renderer *Renderer) sortUsages(usages []model.AccountUsage) []model.AccountUsage {
	sorted := append([]model.AccountUsage(nil), usages...)
	sort.SliceStable(sorted, func(left, right int) bool {
		leftUsage, rightUsage := sorted[left], sorted[right]
		leftReset, leftReachedLimit, leftHasReset := reachedLimitResetAt(leftUsage)
		rightReset, rightReachedLimit, rightHasReset := reachedLimitResetAt(rightUsage)
		if leftReachedLimit != rightReachedLimit {
			return leftReachedLimit
		}
		if !leftReachedLimit {
			return false
		}
		if leftHasReset != rightHasReset {
			return leftHasReset
		}
		return leftHasReset && leftReset.Before(rightReset)
	})
	return sorted
}

// reachedLimitResetAt returns the closest reset among periods that reached 100%.
func reachedLimitResetAt(usage model.AccountUsage) (time.Time, bool, bool) {
	var nearest time.Time
	reached := false
	hasReset := false
	for _, period := range usage.Periods {
		if period.Percent >= 100 {
			reached = true
			if period.ResetAt != nil && (!hasReset || period.ResetAt.Before(nearest)) {
				nearest = *period.ResetAt
				hasReset = true
			}
		}
	}
	return nearest, reached, hasReset
}

func escapeUserText(value string) string {
	value = html.EscapeString(value)
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		"`", "\\`",
		"*", "\\*",
		"_", "\\_",
		"[", "\\[",
		"]", "\\]",
	)
	return replacer.Replace(value)
}

func escapeInlineText(value string) string {
	value = strings.NewReplacer("\r", " ", "\n", " ").Replace(value)
	value = escapeUserText(value)
	return value
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	if maxBytes <= 3 {
		return strings.Repeat(".", maxBytes)
	}
	limit := maxBytes - 3
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit] + "..."
}
