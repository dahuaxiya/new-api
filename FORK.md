# New API 二开维护说明

本仓库是 dahuaxiya/new-api，fork 自 QuantumNous/new-api。后续 New API 定制开发统一在此仓库进行。

- 二开默认分支：`vm2api`。
- 起始版本：上游 `v1.0.0-rc.45`，提交 `6cf0d90b`，与首次额度扩展部署基线一致。
- `origin`：https://github.com/dahuaxiya/new-api.git
- `upstream`：https://github.com/QuantumNous/new-api.git
- `main` 保留上游快照；升级时先在独立分支合并选定上游版本，验证后再合入 `vm2api`，不要覆盖定制分支。

## 当前定制

用户概览和 API 密钥页面显示本人 GPT 订阅的 5 小时 / 7 天额度及重置时间。额度数据由 vm2api 探测，New API 继续负责用户身份和 API 请求。订阅窗口读数不替代 New API 自身的账单额度。

- 前端组件：`web/src/features/gpt-quota/`。
- 接入页面：`web/src/features/keys/index.tsx`、`web/src/features/dashboard/components/overview/overview-dashboard.tsx`。
- 原生后端：`service/gptquota`、`controller/gpt_quota.go`；定时探测由 vm2api 负责。部署样例与旧 Python 回退服务在 `integrations/vm2api-quota/`，详见该目录 README。
- 七种界面的新增翻译键均已登记；简体中文为中文文案，其余语言当前使用英文回退文案。

## 历史统一开户流程

以下保留旧版本的接口和维护说明。当前管理页面已合并为下文的「统一用户管理」，旧 `/users/onboarding` 收藏跳转到用户页的新建抽屉。

超级管理员从管理工作区「用户管理 → 开户」或 `/users/onboarding` 进入。普通用户和普通管理员不显示此入口，后端也会拒绝访问；个人访问令牌不能调用这些接口。

开户顺序：

1. 选择固定登记的 vm2api 实例，选择现有槽位，或指定已配置出口新建本机 Docker GPT 槽位。
2. 未登录槽位点击官方授权，打开 ChatGPT 官方页面完成登录，再将浏览器最终的完整 `http://localhost:1455/auth/callback?...` 地址粘回页面。回调页在本机打不开并不影响复制地址。不要只复制 code，也不要发送密码、回调地址或 token 给其他人。
3. 填写 New API 用户名、显示名、初始密码和站内额度，确认后自动建立普通用户、专属分组、单槽位账号池、上游 Key、渠道及官方额度绑定。
4. 用户登录统一入口，在「API 密钥」创建自己的 New API Key，按 `/docs` 接入。后台的上游 Key 不交给用户。初始站内额度为 0 时不能消费；它与官方 5 小时/7 天额度是两套独立计数。

已有用户及其 Key 不迁移。选择已有的独占槽位时可以复用其单槽位池，原 vm2api Key 仍然有效；本流程不会撤销旧客户端。已被门户用户绑定的槽位、共享池和停用池拒绝再次开户。官方登录成功和槽位就绪不等于真实模型请求已验证，管理员仍需用新用户的 Key 进行一次正常请求确认。

`acct_` 后接 32 位小写十六进制是开户生成的专属分组格式。此类用户只能使用自身分组，不能借助公共分组、特殊加组配置或 Auto 路由转到他人的渠道；服务端在实际 API 请求时同样检查。人工普通分组请勿使用此格式，已有同格式分组也会按专属规则限制。

服务端设置 `VM2API_INSTANCES_CONFIG=/run/vm2api/instances.json`，将私有 JSON 文件以只读方式挂载，文件权限必须为 `0600` 或 `0400`。结构如下，地址和凭据需换成实际配置，不要将真实文件提交到仓库：

```json
{
  "instances": [
    {
      "id": "vps-a",
      "name": "VPS A",
      "base_url": "https://vm-a.example.com",
      "username": "ADMIN_USERNAME",
      "password": "ADMIN_PASSWORD",
      "models": ["MODEL_ID"]
    }
  ]
}
```

`base_url` 是 vm2api 源地址，不带 `/api/panel` 或 `/v1`，远程实例必须使用正常校验证书的 HTTPS。实例 ID 一旦开户就应保持稳定，不能通过改 ID 将同一台实例重新登记。重复源地址会被拒绝。管理地址、凭据和模型列表只由部署配置提供，浏览器只能选择实例 ID。注册表修改后需要重启 New API；新增用户不需要改文件或重启。

旧 `GPT_QUOTA_CONFIG` 的静态用户绑定继续有效；新增用户的实例/槽位关系保存在数据库。额度服务从同一份实例注册表读取后端凭据，因此新增搬瓦工用户不会改动 CloudCone 用户的额度来源。

开户请求按 UUID 持久化，用户和渠道在同一数据库事务内提交；失败时保留原操作，使用原编号重试。初始密码要求 15–128 个字符，通过本地 3000 条常见密码名单校验，原文不会外发；仅保存哈希，开户成功后清除操作记录中的哈希。建槽请求同样使用稳定 UUID；浏览器只保存不含密码和 OAuth 信息的建槽草稿。OAuth 会话仅在内存中保留 10 分钟，绑定管理员、当前登录会话、实例和槽位，一次兑换后失效；服务重启后未完成的官方授权需要重新发起。不会为失败操作自动删除现有槽位或账号池。

这版只提供开户，不包含月费收款、到期停用、套餐续费或账号迁移。出口必须预先在对应 vm2api 配置；本页面不会自动申请或配置 IPv6。

### 开户验证

后端重点检查：`GOTOOLCHAIN=auto go test -race ./service/vm2api ./service/gptquota ./service/onboarding`、`GOTOOLCHAIN=auto go test -race ./controller -run 'Test(GPTQuota|VM2APIOnboarding)'`，以及 `GOTOOLCHAIN=auto go test ./router ./middleware`。模型三库矩阵：`GOTOOLCHAIN=auto go test -race ./model -run '^TestVM2APIEnrollmentDatabases$' -count=1 -v`；MySQL/PostgreSQL 需显式设置 `VM2API_ENROLLMENT_TEST_MYSQL_DSN`、`VM2API_ENROLLMENT_TEST_POSTGRES_DSN`，只允许专用 `enrollment_test` 数据库。验证过 SQLite 3.50.4、MySQL 5.7.44、PostgreSQL 16.13 的新建/升级、两次迁移、唯一约束、租约接管和事务回滚。

安全设计参考 OWASP ASVS v5.0.0 V6/V7/V10，以及 Authentication、Session Management、Password Storage、OAuth2、CSRF Cheat Sheets。重点验证服务端权限和会话撤销、Origin 校验、PKCE/state/当前会话绑定、授权过期与重放、固定目标和禁止重定向、失败响应与 SQL 日志不泄露凭据。此记录不代表完整 ASVS 认证。

## 用户自助 GPT 托管

超级管理员从 `/users` 的用户管理抽屉和「槽位资源」操作，旧 `/users/hosting` 自动跳转；用户入口仍为 `/account/gpt`（我的 GPT 账号）。用户自带个人 GPT 账号，管理员手动收取服务费并发放期限；本版不接支付网关。

1. 管理员在 vm2api 预先创建本机 Docker GPT 槽位、配置出口，保持未登录且不加入任何账号池。托管页按实例读取真实槽位；已登录或属于旧用户的槽位不能转入自助库存。
2. 在托管页将空槽启用为可分配库存。后台检查出口能访问官方身份服务，停止槽位并建立永久隔离标记；库存开关关闭只阻止新分配，不改变已绑定用户。
3. 先创建普通 New API 用户，再按用户 ID 发放一个名额，设置开始、结束和到期保留天数（0–30，页面默认 7）。页面会核对真实用户名，保存需要管理员安全验证。旧静态额度绑定或旧开户用户不自动迁移；管理员设置期限后，经身份预检、确认和二次验证显式接管。
4. 用户在「我的 GPT 账号」经过门户二次验证后点击连接，在 OpenAI 官方页面登录，将完整本地回调地址粘回页面。New API 自动选空槽，核验官方签名身份、检查跨实例重复，然后创建专属渠道和一把上游 Key。服务端不接收 GPT 密码。
5. 用户在 API 密钥页创建客户端 Key；多个 New API Key 共用这个账号、官方额度和上游 Key。用户看不到实例管理凭据或上游 Key。专属分组倍率为 0，月费托管不扣站内钱包；部署须保持 `quota_setting.enable_free_model_pre_consume=false`，客户端 Key 可设为不限站内额度。

一个用户只有一个名额；一个官方账号及个人主体在全部登记实例中最多绑定一个用户。只支持明确的 free/plus/pro 个人身份，无法可靠识别的账号拒绝接入。重登必须是原账号；换账号需要先解绑。旧实例不可达时无法完成跨实例身份去重，新的激活会保留待重试状态。

到期、停用、暂停、未完成登录和清理中都拒绝新请求；API Key、Playground、Responses WebSocket 及重试均检查当前权限。到期保留期内可以续费；保留期结束后后台每 30 秒尝试清理。请求停止按每次请求的实时时间判定，清理不是硬实时任务。首次授权未完成且超过 15 分钟也回收预约；原账号重登保留归属。管理员停用名额即刻禁止门户新请求，凭据仍按既定到期时间及保留期清理。

解绑先禁用访问，再停止 Codex 内核和槽位容器，清掉授权、暂存数据及运行目录，验证成功后才释放账号和槽位；保留槽位设备身份和出口。清理失败保留占用，由后台重试。旧托管 Key 的隔离记录永久保留，即使被手工启用也不能访问后续用户或旧账号。不要从其他面板绕过托管页手工改这些槽位、账号池或 Key。

### 托管部署与回滚

- New API 设置 `GPT_HOSTING_ENABLED=true`，沿用 `VM2API_INSTANCES_CONFIG`。所有登记实例必须先安装扩展；实例 ID 保持稳定。
- vm2api 扩展源在 `integrations/vm2api-hosting/`。基线为 v1.3.202 加已有原生额度补丁，执行 `python3 integrations/vm2api-hosting/install.py /path/to/vm2api-source`；安装器验证精确锚点并可重复运行，不能直接对未知上游版本部署。
- vm2api 必须有稳定的非空 `VM2API_DB_SECRET`，用于加密暂存的 OAuth 令牌。备份 SQLite、配置和加密密钥时单独保管，不放 Git、公开源码包或日志。
- 先备份并部署两个 vm2api，再发布 New API 二进制及静态页面。仅增加托管表，不修改原账号槽位、原 Key 或静态额度映射。
- New API 可回滚镜像和静态页面，保留附加表以免丢失并发用量。已有托管绑定后，不能回滚到不认识托管隔离的 vm2api 代码，也不能通过关闭功能开关来解除用户的到期校验。应先停止新入口，再逐项恢复；数据库备份不是可以随意覆盖在线库的回滚手段。
- 官方登录、同账号重登和新用户真实模型调用需要用户本人完成授权。模拟 OAuth 边界的测试不能证明某个真实新账号已可用。

### 托管验证

- `GOTOOLCHAIN=auto go test ./service/vm2api ./controller ./middleware ./service ./relay -count=1`。
- `GOTOOLCHAIN=auto go test ./model -run '^TestGPTHostingDatabases$' -count=1 -v`；使用前述专用三库 DSN，覆盖 SQLite 3.50.4、MySQL 5.7.44、PostgreSQL 16.13 的新建/旧数据升级/重复迁移、并发抢槽、身份唯一、租约、期限与清理。
- `HOSTING_VM2API_ROOT=/path/to/patched/vm2api node --experimental-test-module-mocks --test integrations/vm2api-hosting/*.test.mjs`。使用真实 SQLite、文件和 Key/池存储；Docker、OAuth 换票和外部身份服务使用受控替身。身份模块另以真实 RSA 签名验证拒绝伪造、错误受众与过期令牌。
- `web/` 执行 `bun run typecheck`、`bun run lint`、相关文件格式检查、`bun run test src/features/gpt-hosting src/features/onboarding src/features/gpt-quota src/features/user-docs` 和 `bun run build`。
- 页面复用了 `SectionPageLayout`、状态组件、`ConfirmDialog`、`SecureVerificationDialog`、React Hook Form、Zod、`GptQuotaCard`，新增组件只组合托管业务。七种语言的键同步登记，主要中文之外使用英文回退。
- 安全参考沿用 OWASP ASVS v5.0.0 V6/V7/V10 和 Authentication、Session Management、OAuth2、CSRF Cheat Sheets；验证了会话/动作绑定、PKCE/state 单次消费、固定官方 OIDC 地址与签名、跨用户隔离、到期拒绝和清理失败保留。此记录不是完整 ASVS 认证。

## 统一用户管理（2026-10-24）

管理员侧栏统一为「用户」：`/users`，内含「用户列表 / 用量统计」。旧开户、GPT 托管和用户用量总览地址分别跳转到新建抽屉、资源抽屉和用量标签，保留原权限限制。

用户列表同时展示普通新用户、旧静态绑定和新托管记录。旧用户显示「期限未设置」，服务继续可用；不会因为安装本版自动获得 30 天期限。托管状态筛选在数据库分页之前执行；当前页最多 100 个账号通过 4 个工作器在 3 秒预算内读取已保存额度，不逐个触发官方探测。未知或读取失败不显示为零。

点击「管理」可查看账号、实例、槽位、官方 5 小时 / 7 天额度，设置一个托管名额的起止时间、到期保留天数及备注，使用 30/90 天续期预填、停用/恢复、调整站内余额和查看最近记录。续期按钮只预填，保存须核对并完成二次验证。保存用户资料与保存托管期限是不同动作。官方额度只读，托管期限、站内余额相互独立。普通管理员可读账号信息，托管写入仍限超级管理员；余额写入复用原权限和账务事务，要求原因及当前会话证明。

创建用户成功后直接打开相同管理抽屉；取消设置期限，用户仍为未开通。槽位资源抽屉复用既有建槽协议，可选择固定实例和预置出口建立空槽并发布库存；此入口不替用户登录官方账号，也不自动建立 IPv6 出口。

旧账号接管核对独占 New API 分组/渠道、远端单槽位 Key、官方签名身份和跨实例唯一性。管理员确认后先保存中央接管意图和唯一归属，再在 vm2api 建立永久隔离、沿用独占上游 Key 和客户端 New API Key。中途失败保留「接管待完成」并重试同一任务，禁止回落到不受期限约束的旧配置。官方 ID token 过期或身份无法验证时，预检不修改服务，必须先由账号本人重新登录原账号。验签期间凭据变化会拒绝接管。

验证包括 SQLite 3.50.4、MySQL 5.7.44、PostgreSQL 16.13 的新建、上一版表结构升级及重复迁移、筛选总数、钱包/客户端 Key 保留、接管恢复和到期阻断；Go race、真实 SQLite 的 vm2api 协议与身份测试；前端权限、预检失败/取消、编辑保护、筛选、余额和桌面/窄屏浏览器交互。敏感交互沿用 OWASP Authentication、Session Management、OAuth2、CSRF Cheat Sheets 的会话、单次证明、Origin、签名和归属控制；不宣称完整 ASVS 认证。

复用现有 DataTablePage、Sheet、ConfirmDialog、SecureVerificationDialog、WindowReading、SlotSetup、钱包事务和审计 API。新增组件只组织统一用户业务，未创建替代的弹层/表格基础设施。新增界面文案已补齐七种语言。

## 用户使用文档

公开入口 `/docs#codex-setup`，包含分平台 Codex 文件配置、API Key、其他客户端、额度说明与常见问题。页面位于 `web/src/features/user-docs/`，从站点配置读取公共 API 地址；示例仅含密钥占位符。页头和用户侧栏均提供入口，保留原有上游文档链接。

## 验证与发布

在 `web/` 执行 `bun install --frozen-lockfile`、`bun run typecheck`、`bun run build`；执行 `bunx vitest run src/features/gpt-quota/__tests__/quota-card.test.tsx src/features/dashboard/components/overview/__tests__/setup-guide.test.tsx` 检查额度和概览回归。

原生模式运行 `GOTOOLCHAIN=auto go test ./service/gptquota ./controller -run 'Test(SelfIsolation|QuotaView|Panel|GPTQuotaSelf)'`。旧 Python 回退服务可在 `integrations/vm2api-quota/` 执行 `python3 -m unittest -v test_server.py`。具体服务配置、同源反向代理和回退方式见该目录 README。nginx 示例需替换域名与证书路径后使用。

本次归档复用已经部署验证的业务代码；仓库推送本身不触发线上发布。今后每次发布记录 Git 提交号，并提供对应版本的源码下载包。

## 凭据与许可证

真实账号映射、管理员密码、API Key、数据库、日志和个人接入资料不得提交。运行配置保留在服务器 `/etc/new-api-quota/config.json`，权限 0600。

保留 New API / QuantumNous 的原有署名与许可证；定制代码按本仓库 AGPL-3.0 许可证分发。
