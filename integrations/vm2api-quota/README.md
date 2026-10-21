# New API 官方 GPT 额度扩展

配套 New API v1.0.0-rc.45（AGPL-3.0），在用户概览和 API 密钥页显示 vm2api 官方窗口快照。保留上游项目名称、版权和许可证。本目录为此次部署的适配服务；前端改动在本仓库的 web/src/features/gpt-quota、keys、dashboard 及 i18n/locales 中。

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
