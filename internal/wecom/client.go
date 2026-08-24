package wecom

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	defaultAttempts  = 3
	maxResponseBytes = 64 << 10
)

type Client struct {
	webhookURL  string
	httpClient  *http.Client
	sleep       func(context.Context, time.Duration) error
	maxAttempts int
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

func New(webhookURL string, options ...Option) *Client {
	client := &Client{
		webhookURL:  webhookURL,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
		sleep:       sleepContext,
		maxAttempts: defaultAttempts,
	}
	for _, option := range options {
		option(client)
	}
	return client
}

func (client *Client) Send(ctx context.Context, messages []string) error {
	for index, message := range messages {
		if err := client.sendMessage(ctx, message); err != nil {
			return fmt.Errorf("发送企业微信消息 %d/%d: %w", index+1, len(messages), err)
		}
	}
	return nil
}

func (client *Client) sendMessage(ctx context.Context, message string) error {
	payload, err := json.Marshal(map[string]any{
		"msgtype": "markdown_v2",
		"markdown_v2": map[string]string{
			"content": message,
		},
	})
	if err != nil {
		return fmt.Errorf("编码企业微信消息: %w", err)
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

func (client *Client) sendOnce(ctx context.Context, payload []byte) (bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.webhookURL, bytes.NewReader(payload))
	if err != nil {
		return false, fmt.Errorf("构造企业微信请求: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "coding-plan-usage/1.0")

	response, err := client.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return true, errors.New("调用企业微信 webhook 网络失败")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return true, fmt.Errorf("读取企业微信响应: %w", err)
	}
	if len(raw) > maxResponseBytes {
		return false, errors.New("企业微信响应超过 64 KiB 限制")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		retryable := response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError
		return retryable, fmt.Errorf("企业微信返回 HTTP %d", response.StatusCode)
	}

	var result struct {
		ErrorCode    int    `json:"errcode"`
		ErrorMessage string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return false, fmt.Errorf("解析企业微信响应: %w", err)
	}
	if result.ErrorCode != 0 {
		retryable := result.ErrorCode == -1 || result.ErrorCode == 45009
		return retryable, fmt.Errorf("企业微信返回 errcode=%d: %s", result.ErrorCode, result.ErrorMessage)
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
