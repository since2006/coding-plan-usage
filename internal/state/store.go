package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"coding-plan-usage/internal/model"
)

const CurrentVersion = 1

type State struct {
	Version       int                    `json:"version"`
	LastDailySlot string                 `json:"last_daily_slot,omitempty"`
	Alerts        map[string]AlertRecord `json:"alerts"`
}

type AlertRecord struct {
	Account        string    `json:"account"`
	Level          string    `json:"level"`
	ResetTimestamp int64     `json:"reset_timestamp"`
	NotifiedAt     time.Time `json:"notified_at"`
}

type Store struct {
	path string
}

func NewStore(path string) *Store { return &Store{path: path} }

func New() State {
	return State{Version: CurrentVersion, Alerts: make(map[string]AlertRecord)}
}

func (store *Store) Load() (State, error) {
	data, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return State{}, fmt.Errorf("读取状态文件: %w", err)
	}
	var value State
	if err := json.Unmarshal(data, &value); err != nil {
		return State{}, fmt.Errorf("解析状态文件: %w", err)
	}
	if value.Version != CurrentVersion {
		return State{}, fmt.Errorf("状态文件 version 必须为 %d", CurrentVersion)
	}
	if value.Alerts == nil {
		value.Alerts = make(map[string]AlertRecord)
	}
	return value, nil
}

func (store *Store) Save(value State) error {
	if value.Version == 0 {
		value.Version = CurrentVersion
	}
	if value.Alerts == nil {
		value.Alerts = make(map[string]AlertRecord)
	}
	directory := filepath.Dir(store.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("创建状态目录: %w", err)
	}

	temporary, err := os.CreateTemp(directory, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时状态文件: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()

	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("设置状态文件权限: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("编码状态文件: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("同步状态文件: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("关闭状态文件: %w", err)
	}
	if err := os.Rename(temporaryName, store.path); err != nil {
		return fmt.Errorf("替换状态文件: %w", err)
	}
	return nil
}

func AlertKey(account string, period model.Period) string {
	reset := strconv.FormatInt(period.ResetTimestamp, 10)
	if period.ResetTimestamp <= 0 {
		reset = "no-reset"
	}
	sum := sha256.Sum256([]byte(account + "\x00" + period.Level + "\x00" + reset))
	return hex.EncodeToString(sum[:])
}

func NewHighPeriods(value State, usages []model.AccountUsage, threshold float64) []AlertRecord {
	records := make([]AlertRecord, 0)
	for _, usage := range usages {
		if usage.Error != "" {
			continue
		}
		for _, period := range usage.Periods {
			if period.Percent <= threshold {
				continue
			}
			key := AlertKey(usage.Account, period)
			if _, exists := value.Alerts[key]; exists {
				continue
			}
			records = append(records, AlertRecord{
				Account:        usage.Account,
				Level:          period.Level,
				ResetTimestamp: period.ResetTimestamp,
			})
		}
	}
	return records
}

func MarkHighPeriods(value *State, usages []model.AccountUsage, threshold float64, notifiedAt time.Time) {
	if value.Alerts == nil {
		value.Alerts = make(map[string]AlertRecord)
	}
	for _, usage := range usages {
		if usage.Error != "" {
			continue
		}
		for _, period := range usage.Periods {
			if period.Percent <= threshold {
				continue
			}
			value.Alerts[AlertKey(usage.Account, period)] = AlertRecord{
				Account:        usage.Account,
				Level:          period.Level,
				ResetTimestamp: period.ResetTimestamp,
				NotifiedAt:     notifiedAt,
			}
		}
	}
}

// RearmAndPrune clears no-reset alerts after usage drops below the threshold and
// removes old reset-window records after a short safety margin.
func RearmAndPrune(value *State, usages []model.AccountUsage, threshold float64, now time.Time) bool {
	changed := false
	for _, usage := range usages {
		if usage.Error != "" {
			continue
		}
		for _, period := range usage.Periods {
			if period.ResetTimestamp <= 0 && period.Percent <= threshold {
				key := AlertKey(usage.Account, period)
				if _, exists := value.Alerts[key]; exists {
					delete(value.Alerts, key)
					changed = true
				}
			}
		}
	}

	cutoff := now.Add(-7 * 24 * time.Hour).Unix()
	for key, record := range value.Alerts {
		if record.ResetTimestamp > 0 && record.ResetTimestamp < cutoff {
			delete(value.Alerts, key)
			changed = true
		}
	}
	return changed
}
