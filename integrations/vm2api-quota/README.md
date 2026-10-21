# New API 官方 GPT 额度扩展

配套 New API v1.0.0-rc.45（AGPL-3.0），在用户概览和 API 密钥页显示 vm2api 官方窗口快照。保留上游项目名称、版权和许可证。本目录为此次部署的适配服务；前端改动在本仓库的 web/src/features/gpt-quota、keys、dashboard 及 i18n/locales 中。

## 原生后端模式（推荐）

额度职责分为两层：vm2api 的 usage-probe monitor 每 60 秒检查槽位，GPT 读数满 300 秒或窗口重置后调用现有官方探测；New API 的 Go 后端通过 `GET /api/gpt-quota/self` 返回当前登录用户绑定槽位的持久化读数。浏览器每 30 秒只更新显示；用户点击“刷新官方额度”时，经登录校验主动查询本人官方窗口。额度查询不发送模型生成请求。

- Go 实现：`service/gptquota`、`controller/gpt_quota.go`；路由使用现有 `UserAuth`，每次校验登录状态及撤销。固定绑定同时匹配用户 ID、用户名、分组。
- `GPT_QUOTA_CONFIG` 指向只读配置文件，结构见 `native-config.example.json`。配置仅供管理员管理，含面板凭据，必须保持 0600，不放入源码包或静态目录。修改绑定后重启 New API。
- 生产 `panel_url` 必须使用有效 TLS；同宿主网络模式可以使用回环 HTTP。禁止关闭证书校验。请求不跟随重定向，不继承环境代理。
- `GET /api/gpt-quota/self` 只读取缓存和持久化快照。面板登录会话复用，过期时只重登一次；额度缓存 15 秒，认证不缓存。故障时保留旧数据并标记错误；没有旧数据返回 503。
- `POST /api/gpt-quota/self/refresh` 使用相同登录校验和固定绑定，调用 `/vms/<固定槽位>/probe`。同槽并发点击合并，同一 New API 进程内相邻探测启动至少间隔 30 秒（成功或失败均计入）。探测使用独立会话，结束注销；调用方断线不取消已启动的有界任务，避免重试引发重复查询。工作限时 70 秒、注销最多再等 5 秒。成功后核实持久化时间并立即更新缓存；失败保留旧读数，返回 503；冷却返回 429。
- 手动刷新限制目前为单进程内存状态；重启会清空，多副本部署需在网关或共享存储实现跨实例合并/冷却。后台 vm2api 定时探测继续运行，不能把前端轮询改为 POST。
- `observed_at` 是 vm2api 样本时间；`last_checked_at` 是 New API 最近读取缓存源的时间，两者不能混用。超过 600 秒或未来时间戳标记过期。`null` 保留未知含义，窗口到期不自动改成 100% 剩余。
- New API 计费、用户 Key、分组和渠道配置保持原语义。本扩展不修改数据库 schema。

构建 Go 后端：`GOTOOLCHAIN=auto CGO_ENABLED=0 go build -o new-api .`，按目标平台设置 `GOOS/GOARCH`。`web/dist` 必须为当前发布的前端构建产物。测试：`GOTOOLCHAIN=auto go test ./service/gptquota ./controller -run 'Test(SelfIsolation|QuotaView|Panel|GPTQuotaSelf)'`。

`native-monitor/` 提供基于固定 vm2api v1.3.202 镜像的增量 Dockerfile，仅替换探测器模块。测试文件应放在完整 vm2api 源码根目录（其 `lib/` 与依赖可用），运行 `node --test usage-probe-native.test.mjs`。探测器保留 Claude 判断、现有单槽合并、并发上限与指数退避。配置 `usage_probe.enabled=true, interval_sec=60, stale_sec=300, concurrency=2`；不要开启会产生模型请求的 `health_probe`。

切换顺序：备份配置与数据库；部署两个后端；确认没有在途推理再重启；验证 Go 接口与登录隔离；删除 nginx 指向 3002 的专用 location，使路径进入 New API；停止 Python 服务，观察至少两轮 vm2api 自动刷新后再 disable。原 Python 文件保留用于回退，不能与原生探测器长期同时轮询。

回退：恢复原两个镜像和 routing 配置、恢复 nginx 的 3002 location、启动 Python 服务。不要回滚用户数据库或恢复旧 Key。公开源码包必须对应当前后端和前端版本，排除真实配置、数据库及凭据。

认证设计参考 OWASP Authentication / Session Management Cheat Sheets：服务器鉴权、撤销生效、凭据不记日志、TLS 传输。针对身份提示伪造、跨用户绑定、未登录、会话注销和禁用用户执行回归检查；这不代表对整个上游项目完成 ASVS 审计。

## 旧独立服务（仅保留回退）

以下为迁移前部署方式；原生模式启用后不要再启动本目录 Python 定时服务。

## 构建与验证

在仓库 web/ 目录执行 bun install --frozen-lockfile、bun run typecheck、bun run build。使用 bunx vitest run src/features/gpt-quota/__tests__/quota-card.test.tsx src/features/dashboard/components/overview/__tests__/setup-guide.test.tsx 运行页面回归测试。在本目录执行 python3 -m unittest -v test_server.py 运行隔离/额度语义测试。

## 服务配置

server.py 只依赖 Python 3.10+ 标准库，默认监听 127.0.0.1:3002。New API 为 127.0.0.1:3001，vm2api 为 127.0.0.1:8787。若部署端口不同，修改源码内固定服务地址；不得改成接受浏览器提交地址的通用代理。

创建 /etc/new-api-quota/config.json，权限 root:root 0600。示例结构如下，所有值须由管理员填写：

```json
{
  "users": {
    "2": {"username": "example", "group": "example", "slot": "vm-example", "label": "示例用户"}
  },
  "vm_login": {"username": "admin", "password": "填写实际密码"}
}
```

每个用户同时校验服务端返回的 id、username、group、status；Bearer 登录态交给 New API 验证，New-Api-User 仅为可选提示。映射变动后重启服务。配置凭据不得进入静态发布目录、公开源码包或版本库。

将 server.py 放入 /opt/new-api-quota，使用随附 systemd 单元启动。LoadCredential 将配置交给 DynamicUser，服务只允许访问回环网络。GET /api/gpt-quota/self 为唯一公共入口，不接受查询参数、槽位编号或面板操作。nginx.conf 展示同源路由方式；证书路径、端口和域名需按部署环境调整。

后台每 300 秒读取已有快照并探测每个固定槽位，查询失败保留带时间的旧读数。windows.used_percent 单位为 0..100；null 表示未知。客户端不得把窗口到期解释为已恢复满额。页面 30 秒取一次缓存，刷新按钮不会触发模型调用。内存缓存重启后重建。

## 发布与回退

发布 bun 构建出的 web/dist，由 nginx 提供静态页面，API 仍转发原 New API 容器。静态目录不得包含 config.json、数据库或真实 Key。公开 quota-source.tar.gz 应包含完整对应源码、此适配服务与构建说明，不含 node_modules、dist、机密配置。

回退时恢复原 nginx 反向代理配置并 reload，停止 new-api-quota 即可。New API 用户、渠道、Key 和数据库无需修改。
