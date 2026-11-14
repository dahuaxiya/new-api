# 商业 API：供货隔离、钱包和对账

本功能复用 New API 的用户、渠道和钱包，让站长的订阅供货与第三方中转服务通过同一入口售卖。客户已有托管账号继续使用专属路由。功能默认关闭；代码验证通过不代表已经配置供货、完成真实收款或达到可售容量。

## 首版范围

- 客户在现有 API 密钥页面创建 `service_mode=retail` 的“余额计费 API”Key。已有 Key 不允许修改业务类型，切换服务需要创建新 Key。
- 上游通过已有渠道页配置，只允许 OpenAI 兼容（type 1）和 Anthropic（type 14）。订阅供货先由 vm2api 转为兼容接口；第三方服务直接接入渠道。
- 客户可调用 `POST /v1/chat/completions`、`POST /v1/responses`、`POST /v1/messages`，支持同步响应及 SSE；可用 `GET /v1/models` 查看可售模型。
- WebSocket、Realtime、Playground、异步任务、compact、count_tokens、图片/音视频专用接口不在本期开放范围。此限制按 API 路径执行，并非对兼容文本接口中所有多模态字段做能力认证。
- 请求须携带完整上下文；任何层级的非空 `previous_response_id`、`encrypted_content`、`conversation` 都会被拒绝。不能据此宣称完整兼容 Codex / Claude Code 的所有功能。
- 账务管理提供 root 会话 API 和渠道页“中转账务”入口，支持三类账本查询与预留释放；尚无自动采购账单导入或实时利润报表。

## 供货配置

环境变量指定一个绝对路径的普通 JSON 文件：

```text
RETAIL_RELAY_CONFIG=/run/new-api/retail.json
```

以下是示例 ID，不能直接照搬到生产：

```json
{
  "enabled": false,
  "group": "retail",
  "supplies": [
    {"channel_id": 101, "kind": "subscription"},
    {"channel_id": 102, "kind": "reseller"}
  ]
}
```

配置限制：文件不超过 64 KiB，不使用符号链接；至多 100 个不重复的正整数渠道 ID；`kind` 只能是 `subscription` 或 `reseller`。开启时必须有供货，组名为 1–64 位字母、数字、下划线或横线，不能为 `auto` 或以 `acct_` 开头。建议文件权限 `0600`，以只读目录挂载。

启用前在渠道页建立站长专用渠道，配置上游地址、Key、模型、映射和优先级，再把渠道加入销售组。销售组还必须存在于分组倍率配置，并为可售模型设置明确售价。不要使用客户托管渠道，哪怕暂时没有流量：带 `acct_` 分组或存在于当前托管绑定表 `account_hosting_bindings` 的渠道会被剔除。

`kind` 仅标记采购归属，不自动登录订阅账号，不验证供应商余额，也不把订阅费换算成逐 token 成本。站长需先分别验证真实 GPT / Claude 账号及第三方接口，才能把它们标为供货。

配置每次选路读取，普通选择、pin、亲和选择及重试共用约束：

- `retail` Key 只能命中有效供货白名单，分组由服务器决定。
- 其他 Key 排除所有已登记商业供货，不能借旧组或历史亲和绕过商业账本。
- `enabled=false` 停止商业调用，但仍隔离 `supplies` 中的渠道。把配置条目删除意味着将该渠道交回旧路由管理，不是单纯停售。
- 未设置环境变量时保留原有业务。显式文件缺失、JSON 损坏或条目非法时，无法安全确定供货边界，会拒绝包括旧 Key 在内的选路。运维应先离线检查 JSON，再在挂载目录中原子替换；不要清空正在读取的文件，也不要通过移除配置解除隔离来处理故障。
- 初期要求 `common.RedisEnabled=false` 且 `common.BatchUpdateEnabled=false`。否则商业 Key 创建及调用关闭；本实现未验收 Redis 缓存和批量余额写回的组合。不要为了开售在其他业务运行期间随意切换全局缓存配置。

托管 Key 的显式模式为 `hosting`，必须拥有合法名额；空模式兼容原版 Key。托管关闭或名额无效时，不允许显式托管 Key 静默转为钱包消费。已有客户账号不会自动变成公共供货。

## 计费与故障处理

`retail_charges` 将钱包与 Key 额度放在同一事务中预留、结算、退款；请求 ID 由服务器生成。该幂等范围是一笔服务器请求，不是客户端重复提交同一提示词的全局去重。

每次上游调用前写入 `retail_attempts`，失败则不发送。每次尝试记录渠道、供货类别、状态和结构化 usage，不保存提示词及响应正文。HTTP 200 的断流会记为 `interrupted`，协议失败或 incomplete 也保留真实状态。

- 明确 429 且没有输出、请求未取消时，最多重试一次；并受原有重试次数和亲和策略约束。5xx、超时、取消或已有输出后不自动重试。
- 客户按本次售价及可核对用量结算一次。缺失或本地估算 usage 时按 0 结算并退还预留，不把估算值当真实上游账单。
- 钱包不足或提交结算失败时不制造负余额，账本保留 `reserved`；消费日志的 `retail_billing_status=pending_review`，不会把计算额当作已结算收入。计算额保存在管理员日志字段中。
- `reserved` 已从可用余额预扣，不能再当作销售收入。进程退出可能留下该状态；没有自动补扣或自动超时退款任务。root 需核实上游结果及是否仍有在途请求，至少 24 小时未更新后才可人工退款。
- 付费模型即使预估为 0，也至少预留 1 个额度单位。预留不是完整输出费用保证；若最终消费超过剩余额度，仍需人工核对。开售前应结合真实模型设置输出上限、并发与速率限制。
- `CostState=unknown` 是未知采购成本，不是 0。供应商账单确认后可登记尝试成本；订阅成本按期间单独登记。币种最小单位不得与钱包 quota 或其他币种直接相加计算利润。

销售统计仍使用原有用户、渠道、消费日志；持久账本是金额核对依据，不能把单独统计日志当作事务凭据。

## 官方支付宝钱包充值

复用已存在的官方支付宝公钥模式，钱包与托管购买使用不同订单表和配置变量：

```text
ALIPAY_WALLET_CONFIG=/run/new-api/alipay-wallet.json
```

```json
{
  "enabled": false,
  "environment": "production",
  "app_id": "REPLACE_WITH_MERCHANT_APP_ID",
  "seller_id": "REPLACE_WITH_SELLER_ID",
  "app_private_key_file": "/run/new-api/alipay-app-private.pem",
  "alipay_public_key_file": "/run/new-api/alipay-public.pem"
}
```

商户 ID 占位符必须替换为真实数字 ID。私钥、配置权限限定为 `0600` / `0400`，不提交到 Git。网关由官方 SDK 固定，不能由浏览器输入；回调和回跳必须使用 HTTPS。应用启动时读取一次支付宝配置，变更后需要按发布流程重启服务。新订单还要求站点已有支付声明确认生效。

充值入口沿用钱包页，提交 `POST /api/user/pay`：

```json
{"amount":1,"payment_method":"alipay_official"}
```

`amount` 沿用站点钱包的展示单位及兑换配置，不是固定人民币分。服务器计算人民币金额并冻结入账 quota；其后修改兑换率不会改变旧订单。

- 通知地址：`POST /api/wallet/alipay/notify`，只接受不超过 64 KiB 的唯一表单字段；拒绝 query 参数、重复字段、错误签名、错误应用/收款人/金额。通知通过 RSA2 验证后才入账。
- 订单归属、交易号唯一约束和数据库事务防止重复入账；通用后台补单不能绕过官方核验。
- 本人补查：`POST /api/user/alipay/query`，JSON `{"order_id":"WP..."}`。只接受有效门户会话并核对订单所有者。
- 后台每分钟有界扫描七日内未决订单，按官方查单结果补偿通知丢失；查单失败保留原状态，前端回跳不发钱。
- 退款等异常通知进入 `paid_review`，不会自动猜测退款金额并扣回客户余额，也不会让后来的支付通知重复入账。需要人工处理，不包含自动退款产品。
- 关闭新订单后，保留原商户配置，以继续核验旧通知/查单。不要在旧订单未处理完时更换商户身份。

本轮未配置真实商户、未发起真实付款；本地签名、回调、防重测试与浏览器模拟页面不能替代真实支付验收。

## root 账务 API

仅允许 root 门户会话；个人访问令牌和普通 API Key 不能调用。写操作还应用同源保护与请求频率限制。使用正常登录的管理会话，不把会话或上游凭据写进脚本示例。

| 接口 | 用途 |
| --- | --- |
| `GET /api/retail/admin/accounting` | 每次至多返回各 100 条 charges、attempts、costs |
| `POST /api/retail/admin/charges/:id/refund` | 人工释放超过 24 小时未更新的预留 |
| `POST /api/retail/admin/costs` | 追加真实采购成本 |

账务查询可用 `request_id` 精确过滤。翻页使用回包 `data.next`：charges 的 `charge_before` 与 `charge_id` 必须一起传；attempts、costs 分别使用 `attempt_before`、`cost_before`。它们是三个独立序列，没有 next 的序列已到尾；不应不断重读它。同秒多笔账单不会仅因时间游标被跳过。按请求过滤时成本只含该请求尝试，订阅期间成本需另查。

退款示例（`expected_reserved` 是读取到的 quota 整数）：

```json
{"expected_reserved":100,"reason":"已核实没有在途请求，供应商未产生费用"}
```

金额必须与账本一致；重复相同审核不重复退款。已结算消费的商业退款不由此接口处理。

逐次成本示例（USD 分，非钱包 quota）：

```json
{"reference":"supplier-invoice-line-001","kind":"attempt","attempt_id":12,"channel_id":102,"currency":"USD","amount_minor":3}
```

订阅成本示例（仅适用于登记为 subscription 的供货）：

```json
{"reference":"subscription-invoice-2026-11","kind":"subscription","channel_id":101,"currency":"USD","amount_minor":2000,"period":"2026-11"}
```

示例金额只是字段演示，不是采购价格建议。`reference` 唯一，重复同一内容可重试，重复凭据但内容不同会拒绝；操作者及时间由服务端填充。首版只追加已核实记录，没有更正/撤销成本接口，不能随意登记估算值。

## 数据与验证

增量为 `tokens.service_mode` 与 `retail_charges`、`retail_attempts`、`retail_costs`、`wallet_payments` 四表；不重建用户、渠道或旧钱包。旧 Key 默认空模式，无自动改组或转收费。

2026-11-14 已验证：

- SQLite 3.50.4、MySQL 5.7.44、PostgreSQL 16.13 的 fresh、重复迁移及并发账务/支付防重。
- 用已发布 `83ae027bedcf4ad6ea4e61d6587863de80412ae7` 源码创建三类数据库，再由新代码连续迁移两次，保留用户余额 4321、Key 额度 321、托管名额和 Key 唯一约束。
- 三协议同步及 SSE、缺失/估算用量、断流、429 重试、5xx 不重试、供货白名单及旧 Key 反向隔离、root 权限/同秒分页/人工退款。
- 前端 6 个测试文件共 29 个测试、类型检查、相关 lint/format 和生产构建；桌面与 390px 手机宽度检查。浏览器接口为本地合成数据。
- Go 构建和相关回归通过。未执行全仓库测试、race、正式并发容量测试或真实供应商/收款端到端验收。

相关回归命令（数据库环境变量必须指向独立测试库，测试会清理该库）：

```sh
GOTOOLCHAIN=auto go test ./model -run '^TestRetailDatabases$' -count=1 -v
GOTOOLCHAIN=auto go test ./controller ./model ./middleware ./router ./service \
  -run 'Test(RetailHTTPRelay|RetailSupplyIsolation|RetailAccountingBoundary|RetailFilterModes|FilterCandidateIDs|ChannelSatisfiesFilters|RetailToken|WalletAlipay|AccessTokenRouteRules|DecideRelayRetry|Pinned|Distribute)' -count=1
GOTOOLCHAIN=auto go test ./relay/channel/openai ./i18n -count=1
GOTOOLCHAIN=auto go build .
cd web
bun run typecheck
bun run test src/features/keys/components/__tests__/api-keys-mutate-drawer.test.tsx src/features/keys/lib src/features/wallet --maxWorkers=2
bun run build
```

未提供 MySQL/PostgreSQL 测试 DSN 时，测试会跳过对应数据库，不能将该结果写为三库通过。发布版真实升级测试另以 Go overlay 注入同一验证程序分别执行旧版 create、新版 verify，不能用删几列模拟替代。

本次鉴权审查参照 OWASP ASVS 5.0.0（稳定标签 `v5.0.0_release`）的 8.1.1、8.2.1、8.2.2、8.3.1：明确 Key 服务类型与供货范围、root 功能权限、本人订单/Key 归属，以及服务端逐请求授权。新增写接口沿用门户会话、同源保护和频率限制，未另建登录或密码流程。相关回归覆盖未登录、普通用户、个人令牌、跨来源、渠道绕过等失败路径；此记录不代表全站 ASVS 认证或已经完成生产渗透测试。

审查参考：

```text
https://github.com/OWASP/ASVS/tree/v5.0.0_release
https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html
https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html
https://cheatsheetseries.owasp.org/cheatsheets/Authorization_Cheat_Sheet.html
```

## 部署前顺序

本轮部署目标仅搬瓦工，不向即将到期的 CloudCone 部署。生产仍运行原 release，本功能尚未发布。

1. 明确站长供货账号、第三方渠道及实际售价；不要将现有客户托管账号纳入供货。
2. 重新核对搬瓦工的当前代码、容量和备份，按根目录 `AGENTS.md` 合入并推送 release，再从远端确切 SHA 构建；取得发布锁并再次核对线上状态。
3. 首次部署保持售卖及支付关闭。验证旧托管、登录、Key、用户数据和增量表，不能用恢复旧库的方式回滚代码。
4. 配置并逐一验证真实上游，再小范围开启商业调用；核对每次尝试、售价、预留和实际扣费。做容量及日志增长观测后再确定并发上限。
5. 配置真实商户，完成一笔实际支付及通知/查单对账，核对旧托管购买未受影响后再开放售卖。
6. 单独处理 CloudCone 剩余非托管槽和旧入口依赖；托管绑定迁空不代表该机器可以直接关机。
