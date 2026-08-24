package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadValidConfigAndResolveStatePath(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	writeConfig(t, path, validConfig)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("warnings = %v", result.Warnings)
	}
	if got, want := result.Config.PollInterval, 10*time.Minute; got != want {
		t.Fatalf("PollInterval = %v, want %v", got, want)
	}
	if got, want := result.Config.State.File, filepath.Join(directory, "data/state.json"); got != want {
		t.Fatalf("State.File = %q, want %q", got, want)
	}
	if result.Config.DailyHour != 9 || result.Config.DailyMinute != 0 {
		t.Fatalf("daily time = %02d:%02d", result.Config.DailyHour, result.Config.DailyMinute)
	}
	if got := result.Config.Accounts[0].Provider; got != ProviderVolcengine {
		t.Fatalf("default provider = %q, want %q", got, ProviderVolcengine)
	}
}

func TestLoadSupportsMixedVolcengineAndZhipuAccounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeConfig(t, path, mixedProviderConfig)
	result, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := result.Config.Accounts[0].Provider, ProviderVolcengine; got != want {
		t.Fatalf("first provider = %q, want %q", got, want)
	}
	if got, want := result.Config.Accounts[1].Provider, ProviderZhipu; got != want {
		t.Fatalf("second provider = %q, want %q", got, want)
	}
	if got := result.Config.Accounts[1].APIKey; got != "zhipu-key" {
		t.Fatalf("zhipu api key = %q", got)
	}
}

func TestLoadRejectsInvalidProviderCredentials(t *testing.T) {
	tests := []struct {
		name    string
		account string
		want    string
	}{
		{
			name:    "unknown provider",
			account: "  - name: bad\n    provider: unknown\n    api_key: key\n",
			want:    "provider 必须为",
		},
		{
			name:    "zhipu missing api key",
			account: "  - name: bad\n    provider: zhipu\n",
			want:    "api_key 不能为空",
		},
		{
			name:    "zhipu mixed credentials",
			account: "  - name: bad\n    provider: zhipu\n    api_key: key\n    access_key_id: AK\n",
			want:    "不能配置 access_key_id",
		},
		{
			name:    "volcengine api key",
			account: "  - name: bad\n    provider: volcengine\n    access_key_id: AK\n    secret_access_key: SK\n    api_key: key\n",
			want:    "火山账号不能配置 api_key",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			content := "version: 1\naccounts:\n" + test.account + configTail
			path := filepath.Join(t.TempDir(), "config.yaml")
			writeConfig(t, path, content)
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestLoadRejectsDuplicateAccountAndUnknownField(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "duplicate account",
			content: strings.Replace(validConfig, "accounts:\n", "accounts:\n  - name: account-a\n    access_key_id: AK2\n    secret_access_key: SK2\n", 1),
			want:    "重复",
		},
		{
			name:    "unknown field",
			content: validConfig + "unknown: true\n",
			want:    "field unknown not found",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			writeConfig(t, path, test.content)
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestLoadWarnsOnBroadPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeConfig(t, path, validConfig)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "chmod 600") {
		t.Fatalf("warnings = %v", result.Warnings)
	}
}

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

const validConfig = `version: 1
accounts:
  - name: account-a
    access_key_id: AK
    secret_access_key: SK
wecom:
  webhook_url: https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test
schedule:
  timezone: Asia/Shanghai
  poll_interval: 10m
  daily_at: "09:00"
alert:
  threshold_percent: 90
state:
  file: ./data/state.json
`

const mixedProviderConfig = `version: 1
accounts:
  - name: volc
    provider: volcengine
    access_key_id: AK
    secret_access_key: SK
  - name: zhipu
    provider: zhipu
    api_key: zhipu-key
` + configTail

const configTail = `wecom:
  webhook_url: https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test
schedule:
  timezone: Asia/Shanghai
  poll_interval: 10m
  daily_at: "09:00"
alert:
  threshold_percent: 90
state:
  file: ./data/state.json
`
