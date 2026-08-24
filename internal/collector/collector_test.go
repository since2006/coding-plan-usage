package collector

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"coding-plan-usage/internal/config"
	"coding-plan-usage/internal/model"
)

type fakeUsageClient struct {
	active    atomic.Int32
	maxActive atomic.Int32
}

func (client *fakeUsageClient) GetCodingPlanUsage(_ context.Context, accessKeyID, secretAccessKey string) (model.Usage, error) {
	active := client.active.Add(1)
	defer client.active.Add(-1)
	for {
		current := client.maxActive.Load()
		if active <= current || client.maxActive.CompareAndSwap(current, active) {
			break
		}
	}
	time.Sleep(5 * time.Millisecond)
	if accessKeyID == "bad-ak" {
		return model.Usage{}, errors.New("AccessDenied for bad-ak with bad-sk")
	}
	return model.Usage{Status: "Running"}, nil
}

type fakeZhipuUsageClient struct {
	calls atomic.Int32
}

func (client *fakeZhipuUsageClient) GetCodingPlanUsage(_ context.Context, apiKey string) (model.Usage, error) {
	client.calls.Add(1)
	return model.Usage{}, errors.New("invalid api key " + apiKey)
}

func TestCollectorLimitsConcurrencyAndRedactsCredentials(t *testing.T) {
	accounts := make([]config.Account, 0, 8)
	for index := 0; index < 7; index++ {
		accounts = append(accounts, config.Account{Name: string(rune('a' + index)), AccessKeyID: "ak", SecretAccessKey: "sk"})
	}
	accounts = append(accounts, config.Account{Name: "bad", AccessKeyID: "bad-ak", SecretAccessKey: "bad-sk"})
	client := &fakeUsageClient{}
	collector := New(accounts, client, WithMaxConcurrency(3))
	results := collector.Collect(context.Background())
	if got := client.maxActive.Load(); got > 3 {
		t.Fatalf("max concurrency = %d", got)
	}
	if len(results) != len(accounts) {
		t.Fatalf("result count = %d", len(results))
	}
	if results[7].Error != "AccessDenied for *** with ***" {
		t.Fatalf("redacted error = %q", results[7].Error)
	}
}

func TestCollectorDispatchesZhipuAccountAndRedactsAPIKey(t *testing.T) {
	zhipuClient := &fakeZhipuUsageClient{}
	accounts := []config.Account{
		{Name: "volc", Provider: config.ProviderVolcengine, AccessKeyID: "ak", SecretAccessKey: "sk"},
		{Name: "zhipu", Provider: config.ProviderZhipu, APIKey: "secret-zhipu-key"},
	}
	collector := New(accounts, &fakeUsageClient{}, WithZhipuClient(zhipuClient))
	results := collector.Collect(context.Background())
	if got := zhipuClient.calls.Load(); got != 1 {
		t.Fatalf("zhipu calls = %d, want 1", got)
	}
	if results[0].Status != "Running" {
		t.Fatalf("volc result = %+v", results[0])
	}
	if got, want := results[1].Error, "invalid api key ***"; got != want {
		t.Fatalf("zhipu error = %q, want %q", got, want)
	}
}
