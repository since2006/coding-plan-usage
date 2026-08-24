package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const CurrentVersion = 1

const (
	ProviderVolcengine = "volcengine"
	ProviderZhipu      = "zhipu"
)

type Config struct {
	Version  int       `yaml:"version"`
	Accounts []Account `yaml:"accounts"`
	WeCom    WeCom     `yaml:"wecom"`
	Schedule Schedule  `yaml:"schedule"`
	Alert    Alert     `yaml:"alert"`
	State    State     `yaml:"state"`

	Location     *time.Location `yaml:"-"`
	PollInterval time.Duration  `yaml:"-"`
	DailyHour    int            `yaml:"-"`
	DailyMinute  int            `yaml:"-"`
}

type Account struct {
	Provider        string `yaml:"provider,omitempty"`
	Name            string `yaml:"name"`
	AccessKeyID     string `yaml:"access_key_id"`
	SecretAccessKey string `yaml:"secret_access_key"`
	APIKey          string `yaml:"api_key"`
}

type WeCom struct {
	WebhookURL string `yaml:"webhook_url"`
}

type Schedule struct {
	Timezone     string `yaml:"timezone"`
	PollInterval string `yaml:"poll_interval"`
	DailyAt      string `yaml:"daily_at"`
}

type Alert struct {
	ThresholdPercent float64 `yaml:"threshold_percent"`
}

type State struct {
	File string `yaml:"file"`
}

type LoadResult struct {
	Config   Config
	Warnings []string
}

func Load(path string) (LoadResult, error) {
	info, err := os.Stat(path)
	if err != nil {
		return LoadResult{}, fmt.Errorf("读取配置文件: %w", err)
	}
	if !info.Mode().IsRegular() {
		return LoadResult{}, errors.New("配置路径不是普通文件")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return LoadResult{}, fmt.Errorf("读取配置文件: %w", err)
	}

	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return LoadResult{}, fmt.Errorf("解析 YAML 配置: %w", err)
	}
	if err := ensureSingleDocument(decoder); err != nil {
		return LoadResult{}, err
	}

	if err := cfg.validate(); err != nil {
		return LoadResult{}, err
	}

	if !filepath.IsAbs(cfg.State.File) {
		absConfig, err := filepath.Abs(path)
		if err != nil {
			return LoadResult{}, fmt.Errorf("解析配置文件绝对路径: %w", err)
		}
		cfg.State.File = filepath.Join(filepath.Dir(absConfig), cfg.State.File)
	}

	warnings := make([]string, 0, 1)
	if info.Mode().Perm()&0o077 != 0 {
		warnings = append(warnings, fmt.Sprintf("配置文件 %s 可被同组或其他用户读取；建议执行 chmod 600", path))
	}

	return LoadResult{Config: cfg, Warnings: warnings}, nil
}

func ensureSingleDocument(decoder *yaml.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("配置文件只能包含一个 YAML 文档")
	}
	return fmt.Errorf("检查 YAML 文档: %w", err)
}

func (cfg *Config) validate() error {
	if cfg.Version != CurrentVersion {
		return fmt.Errorf("version 必须为 %d", CurrentVersion)
	}
	if len(cfg.Accounts) == 0 {
		return errors.New("accounts 至少需要配置一个账号")
	}

	names := make(map[string]struct{}, len(cfg.Accounts))
	for i := range cfg.Accounts {
		account := &cfg.Accounts[i]
		account.Provider = strings.ToLower(strings.TrimSpace(account.Provider))
		account.Name = strings.TrimSpace(account.Name)
		account.AccessKeyID = strings.TrimSpace(account.AccessKeyID)
		account.SecretAccessKey = strings.TrimSpace(account.SecretAccessKey)
		account.APIKey = strings.TrimSpace(account.APIKey)
		if account.Provider == "" {
			account.Provider = ProviderVolcengine
		}
		if account.Name == "" {
			return fmt.Errorf("accounts[%d].name 不能为空", i)
		}
		switch account.Provider {
		case ProviderVolcengine:
			if account.AccessKeyID == "" {
				return fmt.Errorf("accounts[%d].access_key_id 不能为空", i)
			}
			if account.SecretAccessKey == "" {
				return fmt.Errorf("accounts[%d].secret_access_key 不能为空", i)
			}
			if account.APIKey != "" {
				return fmt.Errorf("accounts[%d] 火山账号不能配置 api_key", i)
			}
		case ProviderZhipu:
			if account.APIKey == "" {
				return fmt.Errorf("accounts[%d].api_key 不能为空", i)
			}
			if account.AccessKeyID != "" || account.SecretAccessKey != "" {
				return fmt.Errorf("accounts[%d] 智谱账号不能配置 access_key_id 或 secret_access_key", i)
			}
		default:
			return fmt.Errorf("accounts[%d].provider 必须为 %q 或 %q", i, ProviderVolcengine, ProviderZhipu)
		}
		if _, exists := names[account.Name]; exists {
			return fmt.Errorf("账号名称 %q 重复", account.Name)
		}
		names[account.Name] = struct{}{}
	}

	cfg.WeCom.WebhookURL = strings.TrimSpace(cfg.WeCom.WebhookURL)
	parsedURL, err := url.Parse(cfg.WeCom.WebhookURL)
	if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return errors.New("wecom.webhook_url 必须是有效的 http/https URL")
	}

	if cfg.Schedule.Timezone == "" {
		return errors.New("schedule.timezone 不能为空")
	}
	cfg.Location, err = time.LoadLocation(cfg.Schedule.Timezone)
	if err != nil {
		return fmt.Errorf("schedule.timezone 无效: %w", err)
	}

	cfg.PollInterval, err = time.ParseDuration(cfg.Schedule.PollInterval)
	if err != nil || cfg.PollInterval <= 0 {
		return errors.New("schedule.poll_interval 必须是正数 duration，例如 10m")
	}
	if cfg.PollInterval < time.Minute {
		return errors.New("schedule.poll_interval 不能小于 1m")
	}

	cfg.DailyHour, cfg.DailyMinute, err = parseClock(cfg.Schedule.DailyAt)
	if err != nil {
		return fmt.Errorf("schedule.daily_at: %w", err)
	}

	if cfg.Alert.ThresholdPercent < 0 || cfg.Alert.ThresholdPercent > 100 {
		return errors.New("alert.threshold_percent 必须在 0 到 100 之间")
	}
	cfg.State.File = strings.TrimSpace(cfg.State.File)
	if cfg.State.File == "" {
		return errors.New("state.file 不能为空")
	}
	return nil
}

func parseClock(value string) (int, int, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return 0, 0, errors.New("必须使用 HH:MM 24 小时格式")
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, errors.New("小时必须在 00 到 23 之间")
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, 0, errors.New("分钟必须在 00 到 59 之间")
	}
	return hour, minute, nil
}
