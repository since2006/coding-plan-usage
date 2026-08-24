package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"coding-plan-usage/internal/app"
	"coding-plan-usage/internal/collector"
	"coding-plan-usage/internal/config"
	"coding-plan-usage/internal/feishu"
	"coding-plan-usage/internal/notify"
	"coding-plan-usage/internal/report"
	"coding-plan-usage/internal/state"
	"coding-plan-usage/internal/volc"
	"coding-plan-usage/internal/wecom"
	"coding-plan-usage/internal/zhipu"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(arguments []string) int {
	if len(arguments) == 0 {
		printUsage(os.Stderr)
		return 2
	}

	switch arguments[0] {
	case "run":
		return runDaemon(arguments[1:])
	case "once":
		return runOnce(arguments[1:])
	case "validate":
		return runValidate(arguments[1:])
	case "help", "-h", "--help":
		printUsage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "未知命令 %q\n\n", arguments[0])
		printUsage(os.Stderr)
		return 2
	}
}

func runDaemon(arguments []string) int {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configPath := flags.String("config", "config.yaml", "YAML 配置文件路径")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "run 不接受位置参数")
		return 2
	}

	runtime, err := buildRuntime(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "启动失败：%v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runtime.logger.Info("监控服务已启动", "poll_interval", runtime.config.PollInterval, "daily_at", strings.Join(runtime.config.Schedule.DailyAt, ","), "timezone", runtime.config.Schedule.Timezone)
	if err := runtime.runner.Run(ctx, runtime.config.PollInterval); err != nil {
		fmt.Fprintf(os.Stderr, "服务异常退出：%v\n", err)
		return 1
	}
	runtime.logger.Info("监控服务已停止")
	return 0
}

func runOnce(arguments []string) int {
	flags := flag.NewFlagSet("once", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configPath := flags.String("config", "config.yaml", "YAML 配置文件路径")
	dryRun := flags.Bool("dry-run", false, "只查询并预览，不调用 webhook、不写状态")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "once 不接受位置参数")
		return 2
	}

	runtime, err := buildRuntime(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "启动失败：%v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	mode := app.ModeOnce
	if *dryRun {
		mode = app.ModeDryRun
	}
	outcome, err := runtime.runner.Execute(ctx, mode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "执行失败：%v\n", err)
		return 1
	}
	if *dryRun {
		fmt.Println(strings.Join(outcome.Messages, "\n\n--- 下一条通知消息 ---\n\n"))
	} else {
		fmt.Fprintf(os.Stderr, "推送成功：账号 %d 个，失败 %d 个，消息 %d 条\n", outcome.Successful, outcome.Failed, len(outcome.Messages))
	}
	if outcome.Failed > 0 {
		return 1
	}
	return 0
}

func runValidate(arguments []string) int {
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configPath := flags.String("config", "config.yaml", "YAML 配置文件路径")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "validate 不接受位置参数")
		return 2
	}
	result, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置无效：%v\n", err)
		return 1
	}
	printWarnings(result.Warnings)
	fmt.Printf("配置有效：%d 个账号，轮询间隔 %s，每日推送 %s %s\n", len(result.Config.Accounts), result.Config.PollInterval, result.Config.Schedule.Timezone, strings.Join(result.Config.Schedule.DailyAt, ", "))
	return 0
}

type applicationRuntime struct {
	config config.Config
	runner *app.Runner
	logger *slog.Logger
}

func buildRuntime(configPath string) (*applicationRuntime, error) {
	result, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	printWarnings(result.Warnings)
	cfg := result.Config
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	volcClient := volc.NewClient()
	zhipuClient := zhipu.NewClient()
	usageCollector := collector.New(cfg.Accounts, volcClient, collector.WithZhipuClient(zhipuClient))
	renderer := report.NewRenderer(cfg.Location, cfg.Alert.ThresholdPercent)
	targets := make([]notify.Target, 0, 2)
	if cfg.WeCom.WebhookURL != "" {
		targets = append(targets, notify.Target{Name: "企业微信", Sender: wecom.New(cfg.WeCom.WebhookURL)})
	}
	if cfg.Feishu.WebhookURL != "" {
		targets = append(targets, notify.Target{Name: "飞书", Sender: feishu.New(cfg.Feishu.WebhookURL, cfg.Feishu.Secret)})
	}
	sender := notify.New(targets...)
	store := state.NewStore(cfg.State.File)
	dailyTimes := make([]app.DailyTime, len(cfg.DailyTimes))
	for index, dailyTime := range cfg.DailyTimes {
		dailyTimes[index] = app.DailyTime{Hour: dailyTime.Hour, Minute: dailyTime.Minute}
	}
	runner, err := app.NewRunner(app.RunnerConfig{
		Location:   cfg.Location,
		Threshold:  cfg.Alert.ThresholdPercent,
		DailyTimes: dailyTimes,
	}, usageCollector, sender, renderer, store, logger, time.Now)
	if err != nil {
		return nil, err
	}
	return &applicationRuntime{config: cfg, runner: runner, logger: logger}, nil
}

func printWarnings(warnings []string) {
	for _, warning := range warnings {
		fmt.Fprintf(os.Stderr, "警告：%s\n", warning)
	}
}

func printUsage(output *os.File) {
	fmt.Fprintln(output, "Coding Plan 用量监控")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "用法：")
	fmt.Fprintln(output, "  coding-plan-usage run      --config config.yaml")
	fmt.Fprintln(output, "  coding-plan-usage once     --config config.yaml [--dry-run]")
	fmt.Fprintln(output, "  coding-plan-usage validate --config config.yaml")
}
