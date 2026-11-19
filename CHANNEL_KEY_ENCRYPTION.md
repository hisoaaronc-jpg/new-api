# 渠道密钥加密层（channels.key）— 部署说明

## 这是什么

New API 把上游渠道密钥（`channels` 表的 `key` 列）明文存数据库。数据库一旦泄露
（备份外流、只读副本被读、别处的 SQL 注入），所有上游密钥一起丢。

本改动加一层应用层加密：

- 算法：**AES-256-GCM**（每次加密随机 nonce，密文带完整性校验）
- 存储格式：`enc:v1:<base64(nonce‖密文)>`，前缀即版本号 + “已加密”标记
- 密钥：只来自环境变量 **`CHANNEL_KEY_ENCRYPTION_KEY`**（base64 编码的 32 字节），
  绝不进数据库、绝不进代码仓库
- 历史明文行（无 `enc:v1:` 前缀）读取时原样返回，完全兼容；该行下次写入 key 时
  自动升级为密文（懒迁移，不需要停机迁移脚本）
- 环境变量不设 = 功能关闭，所有读写与上游原版行为一致（可先上镜像、后开功能）
- 环境变量格式错误（非 base64 / 不是 32 字节）= **启动直接失败报错**，不会带病运行
- 密钥错误或未配置却读到密文行 = 报错，绝不静默返回乱码当 key 用

范围：只加密 `channels.key`。`tokens.key`（用户令牌）不在本次范围，仍是明文。

诚实边界：运行中的服务器必须能解密才能调上游，所以这层防的是“数据库单点泄露”，
防不了“服务器本身被攻破”。另外本平台不存位置/语音数据，与本改动无关。

## 改了哪些文件

| 文件 | 改动 |
|---|---|
| `model/channel_key_crypto.go` | **新增**。AES-256-GCM 加/解密、密钥加载与校验、`Channel` 的 GORM 钩子（BeforeCreate/BeforeUpdate 加密、AfterCreate/AfterUpdate/AfterFind 解密；After* 把内存里的值还原成明文，保证缓存和 GetKeys 永远拿到明文；已带前缀的值不再重复加密） |
| `model/channel_plugin_credential.go` | 三处绕过钩子的 map 写 key 路径（`UpdateChannelLocked`、`UpdateTaskPluginChannelLocked`、`UpdateScheduledChannelLocked`）在写入前显式加密；`UpdateChannelLocked` 的 SQLite 分支里 CAS 守卫改为比对数据库中的原始存储值（密文），否则守卫永远匹配不上 |
| `model/main.go` | `InitDB` 里加启动校验：`CHANNEL_KEY_ENCRYPTION_KEY` 格式错误时 InitDB 返回错误、进程拒绝启动 |
| `model/channel_key_crypto_test.go` | **新增**测试：存入是密文/读出是明文、旧明文行可读且轮换后变密文、错密钥报错、无密钥读密文报错、未启用时行为不变、不重复加密、多 key 渠道、密钥格式校验 |

为什么要动 `channel_plugin_credential.go`：这个版本的 New API 里，key 的修改
（轮换、插件登录刷新、定时任务回写）全部走这三个函数的 map 式 `Updates`，
GORM 结构体钩子在 map 更新时不会被调用，只靠钩子会漏掉它们、把明文写回库里。

读路径无需改动：约 61 处直接读 `channel.Key` 的代码拿到的都是 GORM 加载后的
结构体，AfterFind 已解密；出 key 的 `GetKeys()`/`GetNextEnabledKey()` 同理。

## 生成密钥

```bash
openssl rand -base64 32
```

输出形如 `xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx=`（44 字符）。妥善保存它（见下方警告）。

## 镜像构建

在本目录（`nanhai-backend-enc/`，即打了本补丁的源码树）构建，Dockerfile 与上游一致：

```bash
docker build -t nanhai/new-api:enc-v1 .
```

注意：镜像里只有代码，**不要**把密钥写进 Dockerfile 或镜像环境，一律运行时注入。

## Railway 上线步骤

当前线上跑的是官方镜像 `calciumion/new-api:latest`；上线本改动 = 换成自建镜像。
按顺序来，每一步可回滚：

1. **先只换镜像，不设环境变量。** 把 Railway 服务的镜像换成 `nanhai/new-api:enc-v1`
   （或推送到 Railway 可拉取的仓库后引用）。此时加密关闭、行为与现在一致，
   验证服务正常、渠道可调通。
2. **备份密钥。** 生成密钥（见上），存进密码管理器等与数据库无关的地方（见警告）。
3. **加环境变量。** Railway 服务 → Variables → 新增
   `CHANNEL_KEY_ENCRYPTION_KEY` = 上一步生成的值 → 重新部署。
4. **验证。** 重启后：
   - 后台新建一个测试渠道填任意 key，保存；
   - 在 Neon 里查 `SELECT key FROM channels ORDER BY id DESC LIMIT 1;`，
     应看到 `enc:v1:` 开头的一串 base64，而不是明文 key；
   - 用该渠道发一次测试请求，能正常返回即解密链路通；
   - 已有老渠道（明文行）此时仍可正常调用（读时原样返回）。
5. **（可选）全量升级。** 老渠道的 key 行会在各自下一次被写入时自动变密文；
   想立刻全量加密，可在后台逐个打开渠道重新保存 key，或等自然轮换。不做也不影响使用。

回滚：删掉 Railway 的 `CHANNEL_KEY_ENCRYPTION_KEY` 并换回官方镜像即可——
**前提是还没有任何行被写成密文**。一旦已有 `enc:v1:` 行，再回滚到官方镜像
（不认识前缀）会把密文当 key 发给上游、渠道全挂；那种情况下回滚必须保留
自建镜像 + 保留环境变量，或先把密文行逐个改回明文 key 再回滚。

## ⚠️ 密钥备份警告（最重要的一条）

**`CHANNEL_KEY_ENCRYPTION_KEY` 丢失 = 所有已加密的渠道密钥永久不可恢复。**
没有任何后门、没有找回途径，只能把每个上游渠道的 key 重新找回来逐个重填。

- 生成后立刻存进至少两个独立位置（如密码管理器 + 离线备份），不要只存在
  Railway 的变量里——Railway 项目出问题时它会跟着一起丢。
- 不要发到聊天、邮件、代码仓库、工单系统里。
- 换密钥（轮换）不是改个变量就行：旧密文只有旧密钥能解。轮换流程是
  “旧密钥启动 → 把所有渠道 key 读出明文 → 换新密钥 → 逐个重写”，
  没有现成工具前不要贸然轮换。

## 本地验证结果（2026-11-19 实测，Go 1.26.10）

- `go build ./...` 全量通过；`go vet ./model/` 通过；改动文件 gofmt 干净。
  （注：源码树本身不带 `web/dist`，根包的 go:embed 需要它——官方 Dockerfile
  会先构建前端再拷入。本地验证时用占位 index.html 补齐后全量编译通过，
  验证完已移除；Docker 构建不受影响。）
- 新增测试 7 个全过：
  `go test ./model/ -run 'ChannelKeyEncryption|ValidateChannelKeyEncryption' -v`
  - RoundTrip：存入是 `enc:v1:` 密文（不含明文片段）、读出/GetKeys/
    GetNextEnabledKey 都是明文、内存对象写后仍是明文；
  - LegacyPlaintextRow：加密开启前写的明文行照常可读，经
    `UpdateChannelLocked` 轮换后落库变密文（同时证明 SQLite CAS 守卫修复有效）；
  - WrongKeyFails：换错密钥读 → 报错 `cipher: message authentication failed`；
    不设密钥读密文行 → 报错 `channel key is encrypted but
    CHANNEL_KEY_ENCRYPTION_KEY is not set`；换回正确密钥恢复可读；
  - DisabledIsIdentity：未设环境变量时存/读与原版完全一致；
  - NoDoubleEncrypt：加密函数对密文幂等、不动 key 的锁定更新不重写 key 列、
    SaveChannelInfo 后内存与库内值都不变；
  - MultiKey：多 key（换行分隔）整串加密、GetKeys 解出两条、轮换后再加密；
  - ValidateChannelKeyEncryption：非 base64、16 字节短密钥都报错，32 字节通过。
- 回归：`go test ./model/` 全套通过（8.5s）；`go test ./controller/` 全套通过
  （79.6s）。未设环境变量时钩子为 no-op，既有行为不变。
- 数据库兼容性：本改动不改表结构、不改 SQL 形态，只是写入前把字符串换成
  密文字符串，三种数据库（SQLite/MySQL/PostgreSQL）走同一条 GORM 路径；
  本地实测为 SQLite（与项目测试套件相同）。PostgreSQL（线上 Neon）路径与
  SQLite 共用同一套钩子与 map 写入逻辑，但上线前建议在预发 PG 上按上面
  第 4 步再验一遍。
