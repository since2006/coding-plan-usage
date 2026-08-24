package feishu

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultAttempts  = 3
	maxResponseBytes = 64 << 10
)

type Client struct {
	webhookURL  string
	secret      string
	httpClient  *http.Client
	sleep       func(context.Context, time.Duration) error
	maxAttempts int
	now         func() time.Time
}

type Option func(*Client)

func WithHTTPClient(httpClient *http.Client) Option {
	return func(client *Client) {
		if httpClient != nil {
			client.httpClient = httpClient
		}
	}
}

func WithSleeper(sleep func(context.Context, time.Duration) error) Option {
	return func(client *Client) {
		if sleep != nil {
			client.sleep = sleep
		}
	}
}

func WithMaxAttempts(attempts int) Option {
	return func(client *Client) {
		if attempts > 0 {
			client.maxAttempts = attempts
		}
	}
}

func WithClock(now func() time.Time) Option {
	return func(client *Client) {
		if now != nil {
			client.now = now
		}
	}
}

func New(webhookURL, secret string, options ...Option) *Client {
	client := &Client{
		webhookURL:  webhookURL,
		secret:      secret,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
		sleep:       sleepContext,
		maxAttempts: defaultAttempts,
		now:         time.Now,
	}
	for _, option := range options {
		option(client)
	}
	return client
}

func (client *Client) Send(ctx context.Context, messages []string) error {
	for index, message := range messages {
		if err := client.sendMessage(ctx, message); err != nil {
			return fmt.Errorf("发送飞书消息 %d/%d: %w", index+1, len(messages), err)
		}
	}
	return nil
}

func (client *Client) sendMessage(ctx context.Context, message string) error {
	payload, err := client.marshalPayload(message)
	if err != nil {
		return err
	}

	var lastErr error
	for attempt := 1; attempt <= client.maxAttempts; attempt++ {
		retryable, err := client.sendOnce(ctx, payload)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryable || attempt == client.maxAttempts {
			break
		}
		if err := client.sleep(ctx, time.Duration(1<<(attempt-1))*500*time.Millisecond); err != nil {
			return err
		}
	}
	return lastErr
}

func (client *Client) marshalPayload(message string) ([]byte, error) {
	title, content := splitTitle(message)
	payload := map[string]any{
		"msg_type": "interactive",
		"card": map[string]any{
			"config": map[string]bool{"wide_screen_mode": true},
			"header": map[string]any{
				"title": map[string]string{"tag": "plain_text", "content": title},
			},
			"elements": []any{
				map[string]any{
					"tag":  "div",
					"text": map[string]string{"tag": "lark_md", "content": content},
				},
			},
		},
	}
	if client.secret != "" {
		timestamp := client.now().Unix()
		payload["timestamp"] = timestamp
		payload["sign"] = sign(timestamp, client.secret)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("编码飞书消息: %w", err)
	}
	return raw, nil
}

func splitTitle(message string) (string, string) {
	firstLine, content, found := strings.Cut(message, "\n")
	if found && strings.HasPrefix(firstLine, "# ") {
		return strings.TrimPrefix(firstLine, "# "), strings.TrimLeft(content, "\n")
	}
	return "Coding Plan 用量通知", message
}

func sign(timestamp int64, secret string) string {
	value := fmt.Sprintf("%d\n%s", timestamp, secret)
	digest := hmac.New(sha256.New, []byte(value))
	return base64.StdEncoding.EncodeToString(digest.Sum(nil))
}

func (client *Client) sendOnce(ctx context.Context, payload []byte) (bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.webhookURL, bytes.NewReader(payload))
	if err != nil {
		return false, fmt.Errorf("构造飞书请求: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "coding-plan-usage/1.0")

	response, err := client.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return true, errors.New("调用飞书 webhook 网络失败")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return true, fmt.Errorf("读取飞书响应: %w", err)
	}
	if len(raw) > maxResponseBytes {
		return false, errors.New("飞书响应超过 64 KiB 限制")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		retryable := response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError
		return retryable, fmt.Errorf("飞书返回 HTTP %d", response.StatusCode)
	}

	var result struct {
		Code          *int   `json:"code"`
		Message       string `json:"msg"`
		StatusCode    *int   `json:"StatusCode"`
		StatusMessage string `json:"StatusMessage"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return false, fmt.Errorf("解析飞书响应: %w", err)
	}
	if result.Code == nil && result.StatusCode == nil {
		return false, errors.New("飞书响应缺少状态码")
	}
	if result.Code != nil && *result.Code != 0 {
		return *result.Code == -1, fmt.Errorf("飞书返回 code=%d: %s", *result.Code, result.Message)
	}
	if result.StatusCode != nil && *result.StatusCode != 0 {
		return *result.StatusCode == -1, fmt.Errorf("飞书返回 StatusCode=%d: %s", *result.StatusCode, result.StatusMessage)
	}
	return false, nil
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
