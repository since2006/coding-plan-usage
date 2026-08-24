# Coding Plan 多平台用量监控

定时查询多个火山方舟和智谱 GLM Coding Plan 账号的额度快照，并通过企业微信群机器人、飞书群自定义机器人发送汇总。程序统一展示 `session/5小时`、`weekly` 和模型 `monthly` 额度的已用百分比；只有周期严格超过 `alert.threshold_percent` 时才显示距重置剩余时间（如 `15天08时30分钟`），并且只展示每个账号实际返回的周期。

## 功能

- 火山 AK/SK + Signature V4（HMAC-SHA256）调用 `GetCodingPlanUsage`
- 智谱 Coding Plan API Key 查询 5 小时和每周额度，兼容 `TOKENS_LIMIT` 与积分制 `CREDIT_LIMIT`
- 同一进程可混合监控火山和智谱账号
- 最多 5 个账号并发查询，单账号失败不影响整体汇总
- 默认每 10 分钟检查，可配置每天多个固定日报时刻
- 任一账号的一个或多个周期严格大于 90% 时，只发送一次整体提醒和统一账号摘要
- 只对任一周期达到 100% 的账号按该周期的重置时间升序排列；其他账号保留采集顺序
- 按账号、周期、重置时间去重，重启后不会反复提醒
- 企业微信使用 Markdown V2，飞书使用交互式消息卡片；均按平台大小限制自动拆分
- 企业微信、飞书可任选其一，也可同时推送；飞书支持可选的签名校验
- 支持单次推送、只读预览和纯配置校验
- 提供非 root Docker 镜像与 Compose 配置

## 配置

复制示例并填写真实凭证：

```bash
cp config.example.yaml config.yaml
chmod 600 config.yaml
```

```yaml
version: 1

accounts:
  - name: volc-account
    provider: volcengine
    access_key_id: AK...
    secret_access_key: SK...
  - name: zhipu-account
    provider: zhipu
    api_key: ZHIPU_CODING_PLAN_API_KEY...

wecom:
  webhook_url: https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=...

feishu:
  webhook_url: https://open.feishu.cn/open-apis/bot/v2/hook/...
  secret: 飞书机器人签名校验密钥

schedule:
  timezone: Asia/Shanghai
  poll_interval: 10m
  daily_at:
    - "09:00"
    - "18:00"

alert:
  threshold_percent: 90

state:
  file: ./data/state.json
```

`provider` 支持 `volcengine` 和 `zhipu`；为兼容旧配置，省略时默认为 `volcengine`。火山账号必须配置 `access_key_id` 与 `secret_access_key`，智谱账号只配置 `api_key`。

`wecom.webhook_url` 和 `feishu.webhook_url` 至少配置一个。只使用一个通知渠道时，删除另一个渠道的整个配置块；两个都配置时，每次通知会分别发送到两个群。飞书机器人启用了“签名校验”安全设置时必须填写对应的 `feishu.secret`，未启用时可省略 `secret`。

`schedule.daily_at` 必须是 `"HH:MM"` 时刻列表，使用 24 小时制；列表不能为空，时刻不能重复。

相对状态路径以配置文件所在目录为基准。`config.yaml` 同时包含平台凭证、机器人 webhook 和可选签名密钥，已经加入 `.gitignore`，仍应限制为仅运行用户可读。

每组 AK/SK 必须属于要查询的火山账号，并具备方舟用量查询权限。推理使用的 `ark-...` API Key 不能替代控制面 AK/SK。

智谱必须使用 GLM Coding Plan API Key。智谱官方用量查询插件目前明确以个人版套餐为主；团队版 Key 的返回结构和附加请求头没有在公开接口中形成稳定约定，应先使用 `once --dry-run` 验证。

## 使用

本地构建：

```bash
go build -o coding-plan-usage ./cmd/coding-plan-usage
```

配置校验（不访问网络）：

```bash
./coding-plan-usage validate --config config.yaml
```

查询并预览通知摘要，不调用任何 webhook、也不写状态：

```bash
./coding-plan-usage once --config config.yaml --dry-run
```

立即查询并推送完整汇总：

```bash
./coding-plan-usage once --config config.yaml
```

启动常驻调度：

```bash
./coding-plan-usage run --config config.yaml
```

常驻模式启动后立即检查一次。每个日报时刻后如果该时段尚未成功发送，之后每次轮询都会继续补发；如同一天错过多个时刻，只补发最近一个。阈值消息和所有分片全部发送成功后才记录去重状态。

## GitHub Actions 直接部署

`.github/workflows/deploy.yml` 会在代码推送到 `main` 后运行测试和静态检查，交叉编译无 CGO 依赖的 Linux 二进制，通过 `appleboy/scp-action` 上传到服务器，再由 `appleboy/ssh-action` 原子替换目标文件并重启 systemd 服务。也可以在 GitHub Actions 页面手动触发。

仓库需要配置以下 Actions Secrets：

- `DEPLOY_HOST`：服务器域名或 IPv4 地址
- `DEPLOY_USER`：SSH 登录用户
- `DEPLOY_PASSWORD`：SSH 登录密码
- `DEPLOY_FINGERPRINT`：已经核验的服务器 SSH 公钥 SHA256 指纹，例如 `SHA256:...`
- `DEPLOY_PORT`：可选，SSH 端口，默认 `22`

可选的 Actions Variables：

- `DEPLOY_PATH`：二进制安装路径，默认 `/usr/local/bin/coding-plan-usage`
- `DEPLOY_SERVICE`：systemd 服务名，默认 `coding-plan-usage.service`
- `DEPLOY_GOARCH`：服务器架构，支持 `amd64`（默认）或 `arm64`

服务器必须启用 SSH 密码认证，并预先安装、启用对应的 systemd 服务和 `config.yaml`。部署用户必须可以通过无密码 `sudo` 执行 `install`、`mv` 和 `systemctl`；SSH 登录密码不会被用于 `sudo`，权限不足时工作流会直接失败。可以用下面的命令读取服务器 Ed25519 公钥指纹，将输出中的 `SHA256:...` 配置为 `DEPLOY_FINGERPRINT`；设置前仍应通过可信渠道核验：

```bash
ssh-keyscan -p 22 -t ed25519 your-server.example.com 2>/dev/null | ssh-keygen -lf -
```

## Docker Compose

```bash
cp config.example.yaml config.yaml
chmod 600 config.yaml
docker compose up -d --build
docker compose logs -f
```

配置以只读方式挂载到 `/config/config.yaml`，示例中的相对状态路径会落到命名卷 `/config/data/state.json`。常驻模式按单实例设计，不要同时启动多个副本。

## 退出码

- `0`：配置有效或所有账号查询、推送成功
- `1`：配置/状态/API/webhook 失败，或单次执行存在账号查询失败
- `2`：命令或参数错误

常驻模式遇到单轮查询或推送失败时会记录脱敏日志并继续下一轮，不会退出进程。

## 开发验证

```bash
go test -race ./...
go vet ./...
docker build -t coding-plan-usage:test .
```

真实 AK/SK 或 API Key 只应通过本地 `once --dry-run` 做 smoke test，不要写入测试 fixture、提交记录或 CI 变量输出。

## 火山 API 约定

- Endpoint：`POST https://open.volcengineapi.com/`
- Query：`Action=GetCodingPlanUsage&Region=cn-beijing&Version=2024-01-01`
- Signature service/region：`ark` / `cn-beijing`
- 响应：`Result.QuotaUsage[]` 中的 `Level`、`Percent`、`ResetTimestamp`
- `ResetTimestamp <= 0` 表示当前没有可展示的重置时间

实现参考火山引擎的 [Signature V4 文档](https://www.volcengine.com/docs/69190/1400238?lang=zh)、[官方 Go SDK 签名代码](https://github.com/volcengine/volc-sdk-golang/blob/main/base/sign.go)和 [ark-cli Coding Plan 用量说明](https://github.com/volcengine/ark-cli/blob/main/skills/arkcli-usage/references/arkcli-usage-plan.md)。

## 智谱 API 约定

- Endpoint：`GET https://open.bigmodel.cn/api/monitor/usage/quota/limit`
- 鉴权：`Authorization: <GLM Coding Plan API Key>`，不主动添加 `Bearer` 前缀
- 5 小时窗口：`unit=3`、`number=5`，归一化为 `session`
- 每周窗口：`unit=6`、`number=1`，归一化为 `weekly`
- `nextResetTime` 同时兼容秒和毫秒时间戳，内部统一为秒用于提醒去重
- 当 `percentage` 缺失时，尝试通过 `currentValue / usage` 或 `currentValue / (currentValue + remaining)` 计算

该监控端点由智谱官方用量查询插件使用，但未列入版本化的公开 OpenAPI，返回结构可能随套餐升级变化。实现会兼容当前常见的 `TOKENS_LIMIT`、`CREDIT_LIMIT`、对象型及数组型 `data`，接口发生变化时单账号会显示查询失败，不会影响其他账号汇总。

实现参考智谱的 [Coding Plan 套餐说明](https://docs.bigmodel.cn/cn/coding-plan/overview)、[官方用量查询插件说明](https://docs.bigmodel.cn/cn/coding-plan/extension/usage-query-plugin)及其 [查询脚本源码](https://github.com/zai-org/zai-coding-plugins/blob/main/plugins/glm-plan-usage/skills/usage-query-skill/scripts/query-usage.mjs)。
