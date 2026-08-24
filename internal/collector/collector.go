package collector

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"coding-plan-usage/internal/config"
	"coding-plan-usage/internal/model"
)

const defaultMaxConcurrency = 5

type VolcUsageClient interface {
	GetCodingPlanUsage(ctx context.Context, accessKeyID, secretAccessKey string) (model.Usage, error)
}

type ZhipuUsageClient interface {
	GetCodingPlanUsage(ctx context.Context, apiKey string) (model.Usage, error)
}

type Collector struct {
	accounts       []config.Account
	volcClient     VolcUsageClient
	zhipuClient    ZhipuUsageClient
	maxConcurrency int
	now            func() time.Time
}

type Option func(*Collector)

func WithMaxConcurrency(maxConcurrency int) Option {
	return func(collector *Collector) {
		if maxConcurrency > 0 {
			collector.maxConcurrency = maxConcurrency
		}
	}
}

func WithZhipuClient(client ZhipuUsageClient) Option {
	return func(collector *Collector) {
		collector.zhipuClient = client
	}
}

func WithNow(now func() time.Time) Option {
	return func(collector *Collector) {
		if now != nil {
			collector.now = now
		}
	}
}

func New(accounts []config.Account, volcClient VolcUsageClient, options ...Option) *Collector {
	collector := &Collector{
		accounts:       append([]config.Account(nil), accounts...),
		volcClient:     volcClient,
		maxConcurrency: defaultMaxConcurrency,
		now:            time.Now,
	}
	for _, option := range options {
		option(collector)
	}
	return collector
}

func (collector *Collector) Collect(ctx context.Context) []model.AccountUsage {
	results := make([]model.AccountUsage, len(collector.accounts))
	semaphore := make(chan struct{}, collector.maxConcurrency)
	var waitGroup sync.WaitGroup

	for index, account := range collector.accounts {
		index, account := index, account
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results[index] = model.AccountUsage{
					Account:     account.Name,
					CollectedAt: collector.now(),
					Error:       ctx.Err().Error(),
				}
				return
			}

			collectedAt := collector.now()
			var usage model.Usage
			var err error
			provider := account.Provider
			if provider == "" {
				provider = config.ProviderVolcengine
			}
			switch provider {
			case config.ProviderVolcengine:
				if collector.volcClient == nil {
					err = errors.New("火山用量客户端未配置")
				} else {
					usage, err = collector.volcClient.GetCodingPlanUsage(ctx, account.AccessKeyID, account.SecretAccessKey)
				}
			case config.ProviderZhipu:
				if collector.zhipuClient == nil {
					err = errors.New("智谱用量客户端未配置")
				} else {
					usage, err = collector.zhipuClient.GetCodingPlanUsage(ctx, account.APIKey)
				}
			default:
				err = fmt.Errorf("不支持的账号平台 %q", provider)
			}
			if err != nil {
				results[index] = model.AccountUsage{
					Account:     account.Name,
					CollectedAt: collectedAt,
					Error:       redactError(err.Error(), account),
				}
				return
			}
			results[index] = model.AccountUsage{
				Account:     account.Name,
				Status:      usage.Status,
				UpdatedAt:   usage.UpdatedAt,
				Periods:     usage.Periods,
				CollectedAt: collectedAt,
			}
		}()
	}
	waitGroup.Wait()
	return results
}

func redactError(message string, account config.Account) string {
	for _, secret := range []string{account.AccessKeyID, account.SecretAccessKey, account.APIKey} {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "***")
		}
	}
	return message
}
