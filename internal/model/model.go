package model

import (
	"time"
)

const (
	LevelSession    = "session"
	LevelWeekly     = "weekly"
	LevelMonthly    = "monthly"
	LevelMCPMonthly = "mcp_monthly"
)

var CanonicalLevels = []string{LevelSession, LevelWeekly, LevelMonthly, LevelMCPMonthly}

// Period represents one quota window returned by Coding Plan.
type Period struct {
	Level          string
	Percent        float64
	ResetTimestamp int64
	ResetAt        *time.Time
}

// Usage is a provider-neutral Coding Plan quota snapshot.
type Usage struct {
	Status    string
	UpdatedAt *time.Time
	Periods   []Period
}

// AccountUsage is the normalized snapshot for one configured account.
type AccountUsage struct {
	Account     string
	Status      string
	UpdatedAt   *time.Time
	Periods     []Period
	CollectedAt time.Time
	Error       string
}

func (u AccountUsage) IsHigh(threshold float64) bool {
	for _, period := range u.Periods {
		if period.Percent > threshold {
			return true
		}
	}
	return false
}

func (u AccountUsage) Period(level string) (Period, bool) {
	for _, period := range u.Periods {
		if period.Level == level {
			return period, true
		}
	}
	return Period{}, false
}
