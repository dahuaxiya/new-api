"""在指定 vm2api 源目录安装受控覆盖；锚点不匹配立即停止，不能猜测修改新上游版本。"""
from pathlib import Path
import argparse
import shutil


def replace_once(text, old, new):
    """只接受唯一旧锚点或已安装的新锚点，避免重复插入与静默漏装。"""
    if new in text:
        return text
    if text.count(old) != 1:
        raise RuntimeError('Unrecognized vm2api source anchor')
    return text.replace(old, new, 1)


def install(root):
    """先验证全部变更再写入；调用者负责在停用控制面后构建/发布新镜像。"""
    root = root.resolve()
    files = {}
    target = root / 'lib/admin/key-scope.mjs'
    s = target.read_text()
    s = replace_once(s, "import { isDbOpen", "import { hostedAllows } from './hosted-store.mjs'\nimport { isDbOpen")
    s = replace_once(s, 'return applyVmPool(keyScopeFromRecord(req.apiKeyRecord), lookup)', 'return { ...applyVmPool(keyScopeFromRecord(req.apiKeyRecord), lookup), hosted_key_id: req.apiKeyRecord?.id || null }')
    s = replace_once(s, 'export function keyAllowsVm(scope, vm) {', 'export function keyAllowsVm(scope, vm) {\n  // 托管隔离先于普通池规则，master/all 不能越过账户期限和唯一 Key。\n  if (vm?.id && !hostedAllows(scope, vm.id)) return false')
    files[target] = s
    target = root / 'lib/admin/panel-routes.mjs'
    s = target.read_text()
    s = replace_once(s, "import fs from 'node:fs'", "import { handleHostedPanel } from './hosted-panel.mjs'\nimport { hostedRecord } from './hosted-store.mjs'\nimport fs from 'node:fs'")
    anchor = "      if (req.method === 'GET' && p === '/api/panel/me') {"
    s = replace_once(s, anchor, "      if (await handleHostedPanel(ctx, req, res, url, commitImportedCodexVm)) return\n      // 预置托管槽位统一经托管协议修改，避免通用管理接口跳过生命周期。\n      const hostedTarget = p.match(/^\\/api\\/panel\\/vms\\/([^/]+)/)?.[1]\n      if (req.method !== 'GET' && hostedTarget && hostedRecord(hostedTarget)) {\n        return json(res, 409, {ok:false,error:{code:'hosting_managed_slot',message:'Manage this slot through New API hosting'}})\n      }\n" + anchor)
    files[target] = s
    target = root / 'lib/vm/codex-slot.mjs'
    s = target.read_text()
    s = replace_once(s, "import fs from 'node:fs'", "import { assertHostedWrite } from '../admin/hosted-store.mjs'\nimport fs from 'node:fs'")
    s = replace_once(s, 'export function writeCodexAccounts(projectRoot, vmId, accounts) {', 'export function writeCodexAccounts(projectRoot, vmId, accounts) {\n  // 托管账户只允许受控激活/清理写入；后台 token 刷新交给唯一 Codex 内核。\n  assertHostedWrite(vmId)')
    files[target] = s
    target = root / 'lib/oauth/openai-quota.mjs'
    s = target.read_text()
    s = "import { hostedRecord } from '../admin/hosted-store.mjs'\n" + s if "import { hostedRecord }" not in s else s
    anchor = 'async function queryUpstream(slot, { fetchImpl, rotate = true, projectRoot, vmId, policy = null } = {}) {'
    s = replace_once(s, anchor, anchor + '\n  // 托管账号只读官方额度，避免探测与内核同时旋转 refresh token。\n  if (hostedRecord(vmId)) rotate = false')
    files[target] = s
    target = root / 'lib/protocol/handle-codex.mjs'
    s = target.read_text()
    # 该模块已有 node:fs 导入，保持既有协议逻辑，只禁止托管账号第二个刷新者。
    s = "import { hostedRecord } from '../admin/hosted-store.mjs'\n" + s if "import { hostedRecord }" not in s else s
    anchor = "  if (now.access && now.access !== usedAccess) return now.access"
    s = replace_once(s, anchor, anchor + "\n  if (hostedRecord(vm.id)) return '' // 等待唯一内核刷新，不能并发旋转托管授权。")
    files[target] = s
    target = root / 'lib/admin/vm-test-chat.mjs'
    s = target.read_text()
    s = "import { hostedRecord } from './hosted-store.mjs'\n" + s if "import { hostedRecord }" not in s else s
    s = replace_once(s, "live.error === 'upstream_auth' && rotate && refreshToken", "live.error === 'upstream_auth' && rotate && refreshToken && !hostedRecord(target.id)")
    s = replace_once(s, "codex && !result?.ok && looksLikeCodexUnauthorized(result)", "codex && !hostedRecord(vmId) && !result?.ok && looksLikeCodexUnauthorized(result)")
    files[target] = s
    for name in ['hosted-store.mjs', 'hosted-identity.mjs', 'hosted-panel.mjs']:
        files[root / 'lib/admin' / name] = (Path(__file__).parent / name).read_text()
    for target, content in files.items():
        target.write_text(content)
    print('Installed hosting protocol in', root)


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('root', type=Path)
    install(parser.parse_args().root)
