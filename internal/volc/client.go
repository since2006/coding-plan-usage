package volc

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
	defaultEndpoint    = "https://open.volcengineapi.com/"
	defaultRegion      = "cn-beijing"
	defaultService     = "ark"
	defaultVersion     = "2024-01-01"
	defaultAction      = "GetCodingPlanUsage"
	defaultContentType = "application/json; charset=utf-8"
	defaultAttempts    = 3
	maxResponseBytes   = 1 << 20
)

var ErrNoSubscription = errors.New("未发现有效的 Coding Plan 个人版订阅")

type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	Region          string
	Service         string
}

type Client struct {
	endpoint    *url.URL
	httpClient  *http.Client
	now         func() time.Time
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

func WithNow(now func() time.Time) ClientOption {
	return func(client *Client) {
		if now != nil {
			client.now = now
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
		now:         time.Now,
		sleep:       sleepContext,
		maxAttempts: defaultAttempts,
	}
	for _, option := range options {
		option(client)
	}
	return client
}

func (client *Client) GetCodingPlanUsage(ctx context.Context, accessKeyID, secretAccessKey string) (model.Usage, error) {
	credentials := Credentials{
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretAccessKey,
		Region:          defaultRegion,
		Service:         defaultService,
	}

	var lastErr error
	for attempt := 1; attempt <= client.maxAttempts; attempt++ {
		usage, retryable, err := client.getOnce(ctx, credentials)
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

func (client *Client) getOnce(ctx context.Context, credentials Credentials) (model.Usage, bool, error) {
	requestURL := *client.endpoint
	query := requestURL.Query()
	query.Set("Action", defaultAction)
	query.Set("Region", credentials.Region)
	query.Set("Version", defaultVersion)
	requestURL.RawQuery = query.Encode()

	body := []byte{}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), bytes.NewReader(body))
	if err != nil {
		return model.Usage{}, false, fmt.Errorf("构造火山请求: %w", err)
	}
	request.Header.Set("Content-Type", defaultContentType)
	request.Header.Set("User-Agent", "coding-plan-usage/1.0")
	signRequest(request, body, credentials, client.now())

	response, err := client.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return model.Usage{}, false, ctx.Err()
		}
		return model.Usage{}, true, fmt.Errorf("请求火山接口: %w", err)
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return model.Usage{}, true, fmt.Errorf("读取火山响应: %w", err)
	}
	if len(raw) > maxResponseBytes {
		return model.Usage{}, false, errors.New("火山响应超过 1 MiB 限制")
	}

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		apiErr := decodeAPIError(raw)
		if apiErr == nil {
			apiErr = &APIError{StatusCode: response.StatusCode, Message: http.StatusText(response.StatusCode)}
		} else {
			apiErr.StatusCode = response.StatusCode
		}
		retryable := response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError
		return model.Usage{}, retryable, apiErr
	}

	var envelope responseEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return model.Usage{}, false, fmt.Errorf("解析火山响应 JSON: %w", err)
	}
	if envelope.ResponseMetadata.Error != nil {
		return model.Usage{}, false, &APIError{
			StatusCode: response.StatusCode,
			Code:       envelope.ResponseMetadata.Error.Code,
			Message:    envelope.ResponseMetadata.Error.Message,
			RequestID:  envelope.ResponseMetadata.RequestID,
		}
	}

	result := envelope.Result
	if result == nil && len(envelope.QuotaUsage) > 0 {
		result = &usageResult{
			Status:          envelope.Status,
			UpdateTimestamp: envelope.UpdateTimestamp,
			QuotaUsage:      envelope.QuotaUsage,
		}
	}
	if result == nil || len(result.QuotaUsage) == 0 {
		return model.Usage{}, false, ErrNoSubscription
	}

	periods := make([]model.Period, 0, len(result.QuotaUsage))
	for _, quota := range result.QuotaUsage {
		level := strings.ToLower(strings.TrimSpace(quota.Level))
		if !isCanonicalLevel(level) {
			continue
		}
		resetAt := timestampToTime(quota.ResetTimestamp)
		periods = append(periods, model.Period{
			Level:          level,
			Percent:        quota.Percent.Float64(),
			ResetTimestamp: quota.ResetTimestamp,
			ResetAt:        resetAt,
		})
	}
	if len(periods) == 0 {
		return model.Usage{}, false, ErrNoSubscription
	}
	return model.Usage{
		Status:    result.Status,
		UpdatedAt: timestampToTime(result.UpdateTimestamp),
		Periods:   sortPeriods(periods),
	}, false, nil
}

type responseEnvelope struct {
	ResponseMetadata responseMetadata `json:"ResponseMetadata"`
	Result           *usageResult     `json:"Result"`

	Status          string       `json:"Status"`
	UpdateTimestamp int64        `json:"UpdateTimestamp"`
	QuotaUsage      []quotaUsage `json:"QuotaUsage"`
}

type responseMetadata struct {
	RequestID string             `json:"RequestId"`
	Error     *responseErrorBody `json:"Error"`
}

type responseErrorBody struct {
	Code    string `json:"Code"`
	Message string `json:"Message"`
}

type usageResult struct {
	Status          string       `json:"Status"`
	UpdateTimestamp int64        `json:"UpdateTimestamp"`
	QuotaUsage      []quotaUsage `json:"QuotaUsage"`
}

type quotaUsage struct {
	Level          string         `json:"Level"`
	Percent        flexibleNumber `json:"Percent"`
	ResetTimestamp int64          `json:"ResetTimestamp"`
}

type flexibleNumber float64

func (number *flexibleNumber) UnmarshalJSON(data []byte) error {
	var value float64
	if err := json.Unmarshal(data, &value); err == nil {
		*number = flexibleNumber(value)
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return errors.New("百分比必须是数字或数字字符串")
	}
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return errors.New("百分比字符串不是有效数字")
	}
	*number = flexibleNumber(parsed)
	return nil
}

func (number flexibleNumber) Float64() float64 { return float64(number) }

type APIError struct {
	StatusCode int
	Code       string
	Message    string
	RequestID  string
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
		return "火山 OpenAPI 请求失败"
	}
	return strings.Join(parts, ": ")
}

func decodeAPIError(raw []byte) *APIError {
	var envelope responseEnvelope
	if json.Unmarshal(raw, &envelope) != nil || envelope.ResponseMetadata.Error == nil {
		return nil
	}
	return &APIError{
		Code:      envelope.ResponseMetadata.Error.Code,
		Message:   envelope.ResponseMetadata.Error.Message,
		RequestID: envelope.ResponseMetadata.RequestID,
	}
}

func timestampToTime(timestamp int64) *time.Time {
	if timestamp <= 0 {
		return nil
	}
	var value time.Time
	if timestamp < 1_000_000_000_000 {
		value = time.Unix(timestamp, 0)
	} else {
		value = time.UnixMilli(timestamp)
	}
	return &value
}

func isCanonicalLevel(level string) bool {
	return level == model.LevelSession || level == model.LevelWeekly || level == model.LevelMonthly
}

func sortPeriods(periods []model.Period) []model.Period {
	ordered := make([]model.Period, 0, len(periods))
	for _, level := range model.CanonicalLevels {
		for _, period := range periods {
			if period.Level == level {
				ordered = append(ordered, period)
			}
		}
	}
	return ordered
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
