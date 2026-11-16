# 同站点多供货分组

在「渠道 → 供货总览」找到现有供货站点，点击「新增供货分组」。只填写分组名称、该分组自己的 API Key 和模型，不重复填写站点地址。缺少 Key 或模型时创建停用草稿，之后可通过原渠道编辑器补齐并启用。

在「中转定价 → 分组倍率绑定」也可直接打开供货总览。新增后仅刷新渠道候选，保留尚未保存的本站分组、上游倍率组和毛利率。选择对应供货分组的渠道，再保存绑定与毛利。绑定用于计价，不能改变 Key 在供应商处所属的分组。

同一站点下不能重复使用一个 Key 代表不同供货分组。新分组不会复制原渠道凭据、请求头覆盖、模型映射或组织信息。独立分组可分别跟随各自倍率，售价继续使用已保存毛利率、限价和预算。

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
GOTOOLCHAIN=auto go test -p 1 ./model -run '^TestRetail(SupplyGroups|GroupBindings|Pricing)Databases$' -count=1 -v
cd web
./node_modules/.bin/vitest run src/features/channels/components/__tests__/retail-supplies.test.tsx src/features/channels/components/__tests__/retail-group-bindings.test.tsx src/features/channels/components/__tests__/retail-pricing.test.tsx
./node_modules/.bin/tsgo -b
./node_modules/.bin/rsbuild build
```

真实引擎为 SQLite 3.50.4、MySQL 5.7.44、PostgreSQL 16.13。覆盖全新库、模拟上一发布版本缺少新字段的升级、重复启动迁移、保留渠道凭据与 abilities、不同倍率绑定及毛利保留、父地址继承、子地址篡改拒绝、托管隔离、写入失败事务回退。前端 18 个相关用例通过，另检查相关文件 lint/format 和七种语言 translation 命名空间中的新增键。

## 凭据与访问控制

按 OWASP ASVS 5.0.0 范围核对供应商 API 凭据管理；本变更不声称整个系统通过 ASVS 认证。参考 Authentication、Session Management、CSRF Prevention Cheat Sheets：

- https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html
- https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html
- https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html

新增接口位于既有 root 管理路由，复用 DisableCache、会话 Origin 校验和关键操作限流；生产须保持 Secure Cookie 模式及 HTTPS。服务端只接受 site_id/name/key/models，忽略客户端注入地址和父关系；不在成功响应、供货总览、查询缓存返回 Key。Key 不作为 mutation 参数保存；错误仅保留安全消息，去除 Axios 请求对象，提交结束清空表单 Key。测试覆盖匿名 401、普通账号 403、跨 Origin 403、越权父站点、请求体上限及凭据不出现在响应/失败缓存。

凭据持久化复用现有 Channel 存储与既有敏感字段权限，不新增读取凭据或导出接口。继续依赖现有数据库、磁盘和备份的访问限制；本功能未改造全库凭据静态加密或登录的二次验证机制。
