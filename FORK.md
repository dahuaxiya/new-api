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

## 统一开户

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

## 用户使用文档

公开入口 `/docs#codex-setup`，包含分平台 Codex 文件配置、API Key、其他客户端、额度说明与常见问题。页面位于 `web/src/features/user-docs/`，从站点配置读取公共 API 地址；示例仅含密钥占位符。页头和用户侧栏均提供入口，保留原有上游文档链接。

## 验证与发布

在 `web/` 执行 `bun install --frozen-lockfile`、`bun run typecheck`、`bun run build`；执行 `bunx vitest run src/features/gpt-quota/__tests__/quota-card.test.tsx src/features/dashboard/components/overview/__tests__/setup-guide.test.tsx` 检查额度和概览回归。

原生模式运行 `GOTOOLCHAIN=auto go test ./service/gptquota ./controller -run 'Test(SelfIsolation|QuotaView|Panel|GPTQuotaSelf)'`。旧 Python 回退服务可在 `integrations/vm2api-quota/` 执行 `python3 -m unittest -v test_server.py`。具体服务配置、同源反向代理和回退方式见该目录 README。nginx 示例需替换域名与证书路径后使用。

本次归档复用已经部署验证的业务代码；仓库推送本身不触发线上发布。今后每次发布记录 Git 提交号，并提供对应版本的源码下载包。

## 凭据与许可证

真实账号映射、管理员密码、API Key、数据库、日志和个人接入资料不得提交。运行配置保留在服务器 `/etc/new-api-quota/config.json`，权限 0600。

保留 New API / QuantumNous 的原有署名与许可证；定制代码按本仓库 AGPL-3.0 许可证分发。
