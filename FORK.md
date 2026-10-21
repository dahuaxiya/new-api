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
- 后端适配服务、测试及部署样例：`integrations/vm2api-quota/`，详见该目录 README。
- 七种界面的新增翻译键均已登记；简体中文为中文文案，其余语言当前使用英文回退文案。

## 验证与发布

在 `web/` 执行 `bun install --frozen-lockfile`、`bun run typecheck`、`bun run build`；执行 `bunx vitest run src/features/gpt-quota/__tests__/quota-card.test.tsx src/features/dashboard/components/overview/__tests__/setup-guide.test.tsx` 检查额度和概览回归。

在 `integrations/vm2api-quota/` 执行 `python3 -m unittest -v test_server.py`。具体服务配置、同源反向代理和回退方式见该目录 README。nginx 示例需替换域名与证书路径后使用。

本次归档复用已经部署验证的业务代码；仓库推送本身不触发线上发布。今后每次发布记录 Git 提交号，并提供对应版本的源码下载包。

## 凭据与许可证

真实账号映射、管理员密码、API Key、数据库、日志和个人接入资料不得提交。运行配置保留在服务器 `/etc/new-api-quota/config.json`，权限 0600。

保留 New API / QuantumNous 的原有署名与许可证；定制代码按本仓库 AGPL-3.0 许可证分发。
