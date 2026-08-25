package wecom

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
	"unicode/utf8"
)

const botMarkdownLimit = 20480

type BotResponder interface {
	SendMarkdown(ctx context.Context, responseURL, content string) error
}

type BotResponseClient struct {
	httpClient *http.Client
}

func NewBotResponseClient(httpClient *http.Client) *BotResponseClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &BotResponseClient{httpClient: httpClient}
}

func (client *BotResponseClient) SendMarkdown(ctx context.Context, responseURL, content string) error {
	parsedURL, err := url.Parse(responseURL)
	if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return errors.New("企业微信智能机器人 response_url 无效")
	}
	content = truncateBotMarkdown(content, botMarkdownLimit)
	payload, err := json.Marshal(map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"content": content,
		},
	})
	if err != nil {
		return fmt.Errorf("编码企业微信智能机器人回复: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, responseURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("构造企业微信智能机器人回复请求: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "coding-plan-usage/1.0")
	response, err := client.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("调用企业微信智能机器人 response_url 网络失败")
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("读取企业微信智能机器人回复响应: %w", err)
	}
	if len(raw) > maxResponseBytes {
		return errors.New("企业微信智能机器人回复响应超过 64 KiB 限制")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("企业微信智能机器人 response_url 返回 HTTP %d", response.StatusCode)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}

	var result struct {
		ErrorCode    int    `json:"errcode"`
		ErrorMessage string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("解析企业微信智能机器人回复响应: %w", err)
	}
	if result.ErrorCode != 0 {
		return fmt.Errorf("企业微信智能机器人返回 errcode=%d: %s", result.ErrorCode, result.ErrorMessage)
	}
	return nil
}

func truncateBotMarkdown(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	suffix := "\n> 内容过长，已截断"
	limit := maxBytes - len(suffix)
	if limit <= 0 {
		return ""
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit] + suffix
}
