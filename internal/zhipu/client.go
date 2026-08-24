package zhipu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"coding-plan-usage/internal/model"
)

const (
	defaultEndpoint  = "https://open.bigmodel.cn/api/monitor/usage/quota/limit"
	defaultAttempts  = 3
	maxResponseBytes = 1 << 20

	limitTokens = "TOKENS_LIMIT"
	limitCredit = "CREDIT_LIMIT"
)

var ErrNoSubscription = errors.New("未发现有效的智谱 GLM Coding Plan 订阅")

type Client struct {
	endpoint    *url.URL
	httpClient  *http.Client
	sleep       func(context.Context, time.Duration) error
	maxAttempts int
}

type ClientOption func(*Client)

func WithEndpoint(endpoint string) ClientOption {
	return func(client *Client) {
		parsed, err := url.Parse(endpoint)
		if err == nil && parsed.Scheme != "" && parsed.Host != "" {
			client.endpoint = parsed
		}
	}
}

func WithHTTPClient(httpClient *http.Client) ClientOption {
	return func(client *Client) {
		if httpClient != nil {
			client.httpClient = httpClient
		}
	}
}

func WithSleeper(sleep func(context.Context, time.Duration) error) ClientOption {
	return func(client *Client) {
		if sleep != nil {
			client.sleep = sleep
		}
	}
}

func WithMaxAttempts(attempts int) ClientOption {
	return func(client *Client) {
		if attempts > 0 {
			client.maxAttempts = attempts
		}
	}
}

func NewClient(options ...ClientOption) *Client {
	endpoint, _ := url.Parse(defaultEndpoint)
	client := &Client{
		endpoint: endpoint,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		sleep:       sleepContext,
		maxAttempts: defaultAttempts,
	}
	for _, option := range options {
		option(client)
	}
	return client
}

func (client *Client) GetCodingPlanUsage(ctx context.Context, apiKey string) (model.Usage, error) {
	var lastErr error
	for attempt := 1; attempt <= client.maxAttempts; attempt++ {
		usage, retryable, err := client.getOnce(ctx, apiKey)
		if err == nil {
			return usage, nil
		}
		lastErr = err
		if !retryable || attempt == client.maxAttempts {
			break
		}
		if err := client.sleep(ctx, time.Duration(1<<(attempt-1))*500*time.Millisecond); err != nil {
			return model.Usage{}, err
		}
	}
	return model.Usage{}, lastErr
}

func (client *Client) getOnce(ctx context.Context, apiKey string) (model.Usage, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.endpoint.String(), nil)
	if err != nil {
		return model.Usage{}, false, fmt.Errorf("构造智谱请求: %w", err)
	}
	request.Header.Set("Authorization", apiKey)
	request.Header.Set("Accept", "application/json, text/plain, */*")
	request.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	request.Header.Set("User-Agent", "coding-plan-usage/1.0")

	response, err := client.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return model.Usage{}, false, ctx.Err()
		}
		return model.Usage{}, true, fmt.Errorf("请求智谱接口: %w", err)
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return model.Usage{}, true, fmt.Errorf("读取智谱响应: %w", err)
	}
	if len(raw) > maxResponseBytes {
		return model.Usage{}, false, errors.New("智谱响应超过 1 MiB 限制")
	}

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		apiErr := decodeHTTPError(response.StatusCode, raw)
		retryable := response.StatusCode == http.StatusRequestTimeout ||
			response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError
		return model.Usage{}, retryable, apiErr
	}

	data, err := decodeQuotaResponse(raw)
	if err != nil {
		return model.Usage{}, false, err
	}
	periods := normalizeLimits(data.Limits)
	if len(periods) == 0 {
		return model.Usage{}, false, ErrNoSubscription
	}
	return model.Usage{
		Status:  strings.TrimSpace(data.Level),
		Periods: periods,
	}, false, nil
}

type quotaResponse struct {
	Code    flexibleNumber  `json:"code"`
	Success *bool           `json:"success"`
	Msg     string          `json:"msg"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`

	Level  string       `json:"level"`
	Limits []quotaLimit `json:"limits"`
}

type quotaData struct {
	Level    string       `json:"level"`
	PlanName string       `json:"planName"`
	Limits   []quotaLimit `json:"limits"`
}

type quotaLimit struct {
	Type          string         `json:"type"`
	Unit          flexibleNumber `json:"unit"`
	Number        flexibleNumber `json:"number"`
	Usage         flexibleNumber `json:"usage"`
	CurrentValue  flexibleNumber `json:"currentValue"`
	Remaining     flexibleNumber `json:"remaining"`
	Percentage    flexibleNumber `json:"percentage"`
	NextResetTime flexibleNumber `json:"nextResetTime"`
}

type flexibleNumber struct {
	Value float64
	Valid bool
}

func (number *flexibleNumber) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}
	var value float64
	if err := json.Unmarshal(data, &value); err == nil {
		number.Value = value
		number.Valid = true
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return errors.New("数值字段必须是数字或数字字符串")
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil {
		return errors.New("数字字符串不是有效数字")
	}
	number.Value = parsed
	number.Valid = true
	return nil
}

func decodeQuotaResponse(raw []byte) (quotaData, error) {
	var envelope quotaResponse
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return quotaData{}, fmt.Errorf("解析智谱响应 JSON: %w", err)
	}
	if envelope.Success != nil && !*envelope.Success {
		return quotaData{}, responseAPIError(envelope)
	}
	if envelope.Code.Valid && envelope.Code.Value != 0 && envelope.Code.Value != http.StatusOK {
		return quotaData{}, responseAPIError(envelope)
	}

	encodedData := bytes.TrimSpace(envelope.Data)
	if len(encodedData) == 0 || bytes.Equal(encodedData, []byte("null")) {
		return quotaData{Level: envelope.Level, Limits: envelope.Limits}, nil
	}

	switch encodedData[0] {
	case '{':
		var data quotaData
		if err := json.Unmarshal(encodedData, &data); err != nil {
			return quotaData{}, fmt.Errorf("解析智谱额度数据: %w", err)
		}
		if data.Level == "" {
			data.Level = data.PlanName
		}
		return data, nil
	case '[':
		var limits []quotaLimit
		if err := json.Unmarshal(encodedData, &limits); err != nil {
			return quotaData{}, fmt.Errorf("解析智谱额度数据: %w", err)
		}
		return quotaData{Level: envelope.Level, Limits: limits}, nil
	default:
		return quotaData{}, errors.New("解析智谱额度数据: data 必须是对象或数组")
	}
}

func responseAPIError(envelope quotaResponse) *APIError {
	message := strings.TrimSpace(envelope.Msg)
	if message == "" {
		message = strings.TrimSpace(envelope.Message)
	}
	code := ""
	if envelope.Code.Valid {
		code = strconv.FormatFloat(envelope.Code.Value, 'f', -1, 64)
	}
	return &APIError{Code: code, Message: message}
}

func decodeHTTPError(statusCode int, raw []byte) error {
	var envelope quotaResponse
	_ = json.Unmarshal(raw, &envelope)
	apiErr := responseAPIError(envelope)
	apiErr.StatusCode = statusCode
	if apiErr.Message == "" {
		apiErr.Message = http.StatusText(statusCode)
	}
	return apiErr
}

func normalizeLimits(limits []quotaLimit) []model.Period {
	periods := make(map[string]model.Period, 2)
	unclassified := make([]quotaLimit, 0, 2)

	for _, limit := range limits {
		typeName := strings.ToUpper(strings.TrimSpace(limit.Type))
		switch typeName {
		case limitTokens, limitCredit:
			level := tokenWindowLevel(limit)
			if level == "" {
				unclassified = append(unclassified, limit)
				continue
			}
			if period, ok := periodFromLimit(level, limit); ok {
				periods[level] = period
			}
		}
	}

	for _, limit := range unclassified {
		level := model.LevelSession
		if _, exists := periods[level]; exists {
			level = model.LevelWeekly
		}
		if _, exists := periods[level]; exists {
			break
		}
		if period, ok := periodFromLimit(level, limit); ok {
			periods[level] = period
		}
	}

	ordered := make([]model.Period, 0, len(periods))
	for _, level := range model.CanonicalLevels {
		if period, exists := periods[level]; exists {
			ordered = append(ordered, period)
		}
	}
	return ordered
}

func tokenWindowLevel(limit quotaLimit) string {
	switch {
	case isWindow(limit, 3, 5):
		return model.LevelSession
	case isWindow(limit, 6, 1):
		return model.LevelWeekly
	default:
		return ""
	}
}

func isWindow(limit quotaLimit, unit, number int) bool {
	return limit.Unit.Valid && limit.Number.Valid && limit.Unit.Value == float64(unit) && limit.Number.Value == float64(number)
}

func periodFromLimit(level string, limit quotaLimit) (model.Period, bool) {
	percent, ok := limitPercent(limit)
	if !ok {
		return model.Period{}, false
	}
	resetTimestamp, resetAt := normalizeTimestamp(limit.NextResetTime)
	return model.Period{
		Level:          level,
		Percent:        percent,
		ResetTimestamp: resetTimestamp,
		ResetAt:        resetAt,
	}, true
}

func limitPercent(limit quotaLimit) (float64, bool) {
	if limit.Percentage.Valid {
		return limit.Percentage.Value, true
	}
	if limit.CurrentValue.Valid && limit.Usage.Valid && limit.Usage.Value > 0 {
		return limit.CurrentValue.Value / limit.Usage.Value * 100, true
	}
	if limit.CurrentValue.Valid && limit.Remaining.Valid {
		total := limit.CurrentValue.Value + limit.Remaining.Value
		if total > 0 {
			return limit.CurrentValue.Value / total * 100, true
		}
	}
	return 0, false
}

func normalizeTimestamp(number flexibleNumber) (int64, *time.Time) {
	if !number.Valid || number.Value <= 0 {
		return 0, nil
	}
	raw := int64(number.Value)
	var value time.Time
	if raw < 1_000_000_000_000 {
		value = time.Unix(raw, 0)
	} else {
		value = time.UnixMilli(raw)
	}
	return value.Unix(), &value
}

type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (err *APIError) Error() string {
	parts := make([]string, 0, 3)
	if err.StatusCode != 0 {
		parts = append(parts, fmt.Sprintf("HTTP %d", err.StatusCode))
	}
	if err.Code != "" {
		parts = append(parts, err.Code)
	}
	if err.Message != "" {
		parts = append(parts, err.Message)
	}
	if len(parts) == 0 {
		return "智谱用量请求失败"
	}
	return strings.Join(parts, ": ")
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
