# 供货分组与定价

统一入口为「渠道 → 供货与定价」。同一站点的地址只显示一次；点击站点旁的「新增供货分组」，在同页完成以下配置：

1. 选择本站用户组及上游分组。新供货分组名称默认使用上游名称，可修改为本站备注，不会改变上游名称。
2. 填写该上游分组自己的 Key 和允许使用的模型。不重复填写站点地址；已有分组 Key 留空保留原值。
3. 设置全站目标毛利率，查看预计售价倍率，点击「保存绑定与毛利」并确认。分组、凭据、模型 abilities、计价绑定和毛利在同一事务提交，任一环节失败全部回滚。

「添加分组绑定」用于将已有供货凭据再分配给其他本站用户组。同一 Key 仍只能对应一个上游分组。「供货总览」仅查看线路状态；日常星行供货配置统一在本页完成。

未填 Key 或模型的新分组保存为停用草稿；已有停用分组不会因修改 Key 自动启用，需要在渠道列表中明确启用。未绑定的新建草稿 ID 只在本次表单中使用，提交后由服务端分配真实渠道编号。保存后不自动发布官方价表或产品路由，也不自动开启销售。

同一站点下不能重复使用一个 Key 代表不同供货分组。新分组不会复制原渠道凭据、请求头覆盖、模型映射或组织信息。独立分组分别跟随各自倍率，售价继续使用已保存毛利率、限价和预算。

## 兼容与边界

- 站点采用现有部署白名单中的 reseller 渠道作为父节点，原渠道 ID、凭据和既有计价绑定不变；新增分组以 Channel.retail_site_id 关联父渠道。
- 地址、协议从站点继承。通过原渠道编辑接口修改站点地址/协议时，在同一事务更新子分组；直接修改子分组地址/协议会拒绝。
- 新增分组、渠道和 abilities 同事务提交；失败不留下半个分组。数据库迁移新增整数字段及索引，旧渠道保持 0。
- 子分组必须仍与已登记站点的地址、协议、销售组一致，不能混入 acct_ 客户专属组。父渠道被托管占用时，其派生分组不再具备公共供货资格。
- 所有子分组，包括停用、父节点失效、被误改的分组，仍从普通旧钱包/Key 选路中排除，避免绕过零售账本。
- 本功能没有自动发布模型价表、产品路由、改变现有 Key 所属上游分组或开启销售；没有执行任何真实付费模型调用。
- 首版基于已登记的站点添加分组；添加全新供应商站点仍走现有渠道及部署白名单流程。

## 验证

执行过以下验证：

```sh
GOTOOLCHAIN=auto go test -p 1 ./model ./controller ./service -run '^Test(Retail|Channel)' -count=1
# 为三个引擎提供专用测试 DSN 后执行：
GOTOOLCHAIN=auto go test -p 1 ./model -run '^TestRetail(SupplyConfiguration|SupplyGroups|GroupBindings|Pricing)Databases$' -count=1 -v
cd web
./node_modules/.bin/vitest run src/features/channels/components/__tests__/retail-supplies.test.tsx src/features/channels/components/__tests__/retail-group-bindings.test.tsx src/features/channels/components/__tests__/retail-pricing.test.tsx
./node_modules/.bin/tsgo -b
./node_modules/.bin/rsbuild build
```

真实引擎为 SQLite 3.50.4、MySQL 5.7.44、PostgreSQL 16.13。覆盖全新库、模拟上一发布版本缺少新字段的升级、重复启动迁移、保留渠道凭据与 abilities、不同倍率绑定及毛利保留、父地址继承、子地址篡改拒绝、托管隔离、写入失败事务回退。供货、定价、换账号及导航前端 40 个相关用例通过，另检查相关文件 lint/format 和七种语言 translation 命名空间中的新增键。

## 凭据与访问控制

按 OWASP ASVS 5.0.0 范围核对供应商 API 凭据管理；本变更不声称整个系统通过 ASVS 认证。参考 Authentication、Session Management、CSRF Prevention Cheat Sheets：

- https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html
- https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html
- https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html

新增接口位于既有 root 管理路由，复用 DisableCache、会话 Origin 校验和关键操作限流；生产须保持 Secure Cookie 模式及 HTTPS。服务端只接受 site_id/name/key/models，忽略客户端注入地址和父关系；不在成功响应、供货总览、查询缓存返回 Key。Key 不作为 mutation 参数保存；错误仅保留安全消息，去除 Axios 请求对象，提交结束清空表单 Key。测试覆盖匿名 401、普通账号 403、跨 Origin 403、越权父站点、请求体上限及凭据不出现在响应/失败缓存。

凭据持久化复用现有 Channel 存储与既有敏感字段权限，不新增读取凭据或导出接口。继续依赖现有数据库、磁盘和备份的访问限制；本功能未改造全库凭据静态加密或登录的二次验证机制。

## 统一账号切换

中转站登录页提示账号密码在统一登录页输入。身份源授权页显示当前账号及「使用其他账号登录」按钮；点击后复用退出确认窗口，通过现有 POST 注销接口撤销身份源当前会话，成功后才清理浏览器认证状态，并前往账号密码输入页。原授权路径中的 state、challenge、client_id 与 login 用途被保留，成功登录后继续返回中转站。

不通过 GET、页面加载或 URL 参数触发注销；注销失败保留当前身份，不假装已切换。退出组件的返回地址复用 sanitizeAuthRedirect，只允许同源路径。敏感操作的 verify 授权不显示换号入口，不能通过换身份完成原操作。未改动 MFA、PKCE、一次性授权码、Origin 校验或独立站点资产边界。依据 OWASP ASVS 5.0.0 及上述 Authentication、Session Management、CSRF 指南验证相关控制，不声称完整合规。

身份源只发布前端；release.json 记录真实旧后端提交。中转站同步发布配套的原子保存接口与前端。线上不会为真实 Key 猜测上游归属，浏览器配置回归使用隔离本地合成数据，未产生付费模型调用。

### 让本站分组跟随上游模型

在「渠道 → 中转定价 → 分组倍率绑定」找到要跟随的那一行，打开「跟随该供货分组的模型」，保存绑定。现有绑定默认关闭。开放销售后，后台每 10 分钟用星行该供货渠道自己的单个 Key 读取 `/v1/models`，不使用公开倍率接口推断模型，也不发送模型调用。首版支持标准 Bearer 鉴权的 OpenAI 兼容渠道；多 Key 池、禁用渠道和自定义鉴权不参与自动发布。

已核验且未过期的模型按原有分组倍率和毛利计价，使用原来的分组 Key 或工作台即可。新型号没有有效官方价、映射有歧义时显示「待确认模型」。上游连续两次完整清单缺少已有型号，仅暂停本线路；返回后自动恢复。获取失败、空清单、分页未完不会下架模型。关闭跟随后停止更新，已暂停的线路保持暂停。这里不改变用户权限、Key 模型白名单、余额或已有消费保护。
