# 德国客户合规升级（DE upgrade）

在渠道密钥加密层（`CHANNEL_KEY_ENCRYPTION.md`，AES-256-GCM，`enc:v1:`）之上补四处
平台缺口。实现基于本仓库现有机制，不改表结构以外的兼容性承诺（新增一列
`tokens.log_disabled`，由 `AutoMigrate` 自动补列）。

## 1. 上游 key 轮换流程

接口：`POST /api/channel/:id/rotate_key`，权限 `channel:sensitive_write`
（与新建/删除渠道同级），管理员鉴权。

请求体：

```json
{ "key": "sk-新的上游密钥", "model": "gpt-4o-mini", "endpoint_type": "" }
```

`model` / `endpoint_type` 可选，不填时用渠道配置的测试模型（与现有“测试渠道”
同一逻辑）。

流程：

1. 用新 key 对该渠道的**副本**做一次真实测试调用（复用 `testChannel`，
   与 `GET /api/channel/test/:id` 完全相同的测试路径）。
2. 测试通过，才经 `UpdateChannelLocked` 原子替换数据库里的 key；该路径已由
   加密层处理，存进库的是 `enc:v1:` 密文。
3. 写一条管理员审计日志（`channel.rotate_key`，含渠道 id 与名称，不含密钥值）。
4. 测试不通过，旧 key 原样不动，接口返回 `success: false` 及上游报错。

限制（有意为之）：

- **多 key 渠道拒绝轮换**：多 key 渠道存的是 key 列表，拿单个新 key 整体替换会
  静默丢掉其他 key。遇到多 key 渠道请走渠道编辑页改 key 列表。
- 轮换 ≠ 自动向上游申请新 key：新 key 必须由管理员从上游处取得后提交，系统只
  负责“验证后无缝替换”。

## 2. 日志保留期自动清理

原有 `log_cleanup` 系统任务只支持管理员手动触发；现已按项目已有的定时任务机制
（`ScheduledSystemTaskHandler`，与 `system_task_cleanup` 相同模式）接入调度器：

- 每天执行一次，删除 `created_at` 早于保留期截止点的日志行（分批删除，复用
  现有 `runLogCleanupTask`，含 ClickHouse 的单次 mutation 路径）。
- 保留期由环境变量 **`LOG_RETENTION_DAYS`**（天数）配置，**默认 30 天**。
  未设置、解析失败或 ≤0 时回退到 30 天——写错值不会在下一次运行清空日志表。
- 手动触发（`POST /api/system/task/log_cleanup?target_timestamp=...`）保持不变；
  手动清理后，调度器的下一次运行按最近一次任务的更新时间顺延 24 小时。

## 3. 按 token 关用量日志

Token 新增开关字段 **`log_disabled`**（`tokens.log_disabled`，默认关闭）：

- 开关位置：令牌的新建/编辑接口（`POST /api/token`、`PUT /api/token`）的
  `log_disabled` 字段；前端未加开关前也可直接改库。
- 开关打开时：该 token 的请求**不写用量日志行**（`RecordConsumeLog` 与
  `RecordTaskBillingLog` 的 consume 类型都跳过），用量数据导出（`LogQuotaData`）
  同样跳过——这是同一批用量数据，不应从另一个出口漏出去。
- **计费/扣额照常**：扣费发生在日志写入之前（`service/quota.go` 里
  `SettleBilling` 在 `RecordConsumeLog` 之前调用），日志函数本身从不参与扣费；
  开关只能让日志行消失，不能让一次扣费消失。配额超限告警（`attachQuotaSaturation`
  的后端日志警告）也在 `RecordConsumeLog` 之前触发，不受开关影响。
- 开关关闭时：行为与现在完全一致。
- 查不到 token 或查库失败时默认继续写日志：开关绝不因一次查询失败而静默丢日志。

## 4. 客户对账单接口

对账单直接聚合 `logs` 表里的用量日志（`type = LogTypeConsume`，即
`RecordConsumeLog` 已写入的行），不新建任何计费逻辑，不读、不改、不碰结算。

接口（都在 `/api/log` 下，沿用日志接口的鉴权风格）：

| 接口 | 权限 | 说明 |
|---|---|---|
| `GET /api/log/statement` | 管理员 | 查任意用户：`user_id` 指定某客户，不传则汇总全部用户 |
| `GET /api/log/self/statement` | 登录用户 | 只查自己的；传 `user_id` 也会被忽略 |
| `GET /api/log/statement/export` | 管理员 | 同上，下载 CSV |
| `GET /api/log/self/statement/export` | 登录用户 | 同上，下载自己的 CSV |

参数（二选一，都不传则默认当月）：

- `month=YYYY-MM`：整月，起止为服务器本地时间的月初 00:00:00 到月末 23:59:59。
- `start_timestamp` / `end_timestamp`（Unix 秒，起止都含）：任意时间窗。
- `month` 格式不对、起止颠倒时返回 `success: false`。

返回 `data`：

```json
{
  "month": "2026-11",
  "start_timestamp": 1761955200,
  "end_timestamp": 1764547199,
  "user_id": 12,
  "by_model":   [{ "model_name": "gpt-4o", "requests": 2, "prompt_tokens": 300, "completion_tokens": 150, "token_used": 450, "quota": 3000 }],
  "by_channel": [{ "channel_id": 1, "channel_name": "chan-one", "requests": 2, "prompt_tokens": 300, "completion_tokens": 150, "token_used": 450, "quota": 3000 }],
  "total":      { "requests": 3, "prompt_tokens": 310, "completion_tokens": 160, "token_used": 470, "quota": 3500 }
}
```

`quota` 是平台计费单位（`common.QuotaPerUnit` quota = $1），金额换算由控制台
前端做，接口本身不返回美元金额。`by_model` / `by_channel` 按 `quota` 降序；
已删除的渠道只剩 `channel_id`，`channel_name` 为空。

CSV 导出：`text/csv`，UTF-8 BOM（Excel 可直接打开），列为
`group_type,model_name,channel_id,channel_name,requests,prompt_tokens,completion_tokens,token_used,quota`，
先是 `model` 行，再是 `channel` 行，最后是一行 `total`。文件名
`statement-YYYYMMDD-HHMMSS.csv`。

权限与范围：

- 普通用户只能看到自己的数：`user_id` 参数在个人接口上直接忽略；时间窗对
  非管理员同样受日志最长查询范围（`log_setting.max_range_days`，默认 365 天）
  限制，与日志查询一致。
- 只有已写入的用量日志才会被统计：`log_disabled` 的 token 不产生日志行，
  其用量也不会出现在对账单里——扣费本身照常发生，只是逐笔记录缺失（见第 3 节）。

实现位置：聚合函数 `model.GetStatement`（`model/log.go`，三条普通 `GROUP BY`
查询，SQLite/MySQL/PostgreSQL 通用）；接口与 CSV 在 `controller/statement.go`；
路由在 `router/api-router.go` 的 `logRoute` 组；access-token 规则在
`middleware/access_token_routes.go`（与同组日志路由一致）。

## 测试结果

环境：本机 Go 1.26.10（`~/go-install/go`），SQLite（与项目测试套件相同），
`GOTMPDIR` 指到 home 下（本机 `/tmp` 只有 512M，`go test` 编译会写满）。

- `gofmt` 干净；`go vet ./model/ ./service/ ./controller/ ./router/` 通过。
- `go build ./model/ ./service/ ./controller/ ./router/` 通过。根目录
  `go build ./...` 失败与本次改动无关：`main.go` 嵌了 `web/dist`，而本工作区
  从未构建过前端，`web/dist` 不存在（改动前即如此）。
- 加密回归：`go test ./model/ -run 'ChannelKeyEncryption|TokenLogDisabled|ValidateChannelKeyEncryption'`——11 个全过（加密 7 个 + token 开关 4 个）。
- `go test ./model/` 全量：通过（13.6s，含第 4 项改动后重跑）。
- 日志清理：`go test ./service/ -run 'TestLogCleanup|TestSystemTaskScheduler|TestSystemTaskClaimPass|TestEnqueueSystemTask'`——8 个全过（清理 3 个 + 调度器 5 个）。
- 轮换：`go test ./controller/ -run 'TestRotateChannelKey'`——6 个全过（25.9s）。
- 路由：`go test ./router/` 全量通过（17.5s）。新路由另需在
  `middleware/access_token_routes.go` 里登记 access-token 规则（管理员
  `log:read`、个人 `usage:read`，与同组日志路由一致），否则
  `TestAccessTokenRouteRulesCoverEveryDashboardRoute` 会失败——已补。
- 对账单：`go test ./controller/ -run 'TestUserLogStatement|TestLogStatement'`——5 个全过（37.7s）：普通用户只能查自己（`user_id` 参数被忽略）、管理员可按 `user_id` 查任意用户及汇总、聚合数字与造的日志数据一致（窗口外与非 consume 行被排除）、月份格式与颠倒时间窗被拒绝、CSV 含 BOM 与 TOTAL 行且不含他人数据。
- `go test ./controller/` 全量（492 个测试）：本机只有 1.5 个共享 CPU，单测
  每个都要初始化 SQLite，跑满 10 分钟/20 分钟超时两次均未发现失败测试——
  超时是吞吐量问题，不是回归。改用 45 分钟超时重跑后**全部通过**
  （`ok 1590.091s`，含第 1–3 项代码）。第 4 项代码加入后又以同样方式重跑
  了一次，同样全部通过（`ok 1474.730s`，497 个测试）。随后修复了两处回归
  （见下）并以最终代码再跑一次，全部通过（`ok 742.985s`）。
- `go test ./service/` 全量：通过（15.9s）。中途发现并修了两处由本次改动
  引入的回归（见下），修完后全量通过。
- `go test ./middleware/` 全量：通过（40.9s）。

**回归复盘（都已修复并由全量套件验证）：**

1. `UpdateChannelLocked` 的 CAS 守卫用 `Select(commonKeyCol)` 读原始 key
   值：`commonKeyCol` 只由 `InitDB` 里的 `initCol()` 设置，直接开库的调用方
   （如插件刷新测试用的 `tpp_*` 前缀库）拿到的 `Select("")` 会生成
   `SELECT FROM …` 语法错误，导致 8 个插件刷新测试失败。已改为按结构体字段
   名 `Select("Key")`，由 GORM 自行处理列名引用，不再依赖该全局变量。
2. `model/log.go` 里曾顺带加了 `LOG_STORE_CONTENT` 内容丢弃（日志行默认不存
   报文内容）：不在任务范围内，且与 2 个现有测试（任务计费日志的 Content
   应含 "Billing parameters: …"）冲突。已整段删掉，恢复
   `other.setContent(params.Content)`；第 3 项只保留 `log_disabled` 开关本身。

## 没做到的部分

- **前端开关 UI**：`log_disabled` 的开关只做到 API 层（新建/编辑令牌接口已接受
  该字段），Web 控制台的令牌表单尚未加对应的开关控件——需要前端改动，未包含
  在本次后端任务里。
- **多 key 渠道轮换**：见第 1 节限制，需产品侧定逐 key 轮换的交互后再做。
- **日志保留期的管理界面配置**：保留期目前只认环境变量，未接入操作设置页；
  改保留期需要改环境变量并重启。
- **MySQL / PostgreSQL 实机验证**：本地验证为 SQLite（与项目测试套件相同）。
  新增列与四处改动走的都是 GORM 通用路径，未用方言特性；按项目规则，上线前
  仍应在预发 MySQL 与 PostgreSQL 上各跑一次启动迁移。
- **对账单前端页面**：接口已就绪，Web 控制台尚未加“我的对账单”页面——
  需要前端改动，未包含在本次后端任务里。
- **对账单不含美元金额**：接口与 CSV 只给 `quota`（平台计费单位），金额换算
  由控制台前端按 `common.QuotaPerUnit` 做；CSV 里也没有 `amount_usd` 列。
- **仓库里已有一套未接路由的旧对账单实现**：`model.GetStatementRows` 与
  `controller.GetStatement` / `ExportStatement`（`usedata.go`）读的是
  `quota_data` 小时聚合表（有导出延迟，且 `log_disabled` 的用量同样缺失），
  且从未注册路由。本次按任务要求新建了直接读 `logs` 表的接口，未动旧代码；
  是否删除或统一这两套实现，需主代理定夺。
- **对账单与 `log_disabled` 的关系**：见第 4 节——开了开关的 token 用量不进
  对账单，这是“不写日志”的直接后果，不是统计 bug；如需对账单包含这部分
  用量，只能从结算侧（已扣费记录）另取数，那是另一个功能。
