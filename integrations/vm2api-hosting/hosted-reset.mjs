/** 官方重置次数只通过已核验的托管槽位查询和消费；不向门户返回账号凭据或上游原文。 */
import { queryOpenaiQuota, resetOpenaiQuota } from '../oauth/openai-quota.mjs'
import { queryClaudeResetCredits, redeemClaudeResetCredit } from '../oauth/claude-reset-credits.mjs'
import { isClaudeVm, isCodexVm } from '../vm/vm-kind.mjs'
import { hostedError } from './hosted-identity.mjs'

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/

/**
 * 限定日期和文本长度，避免将上游未知字段透传到用户页面。
 * @param value 官方接口提供的日期。
 * @return 可解析且长度受限的日期，否则返回空字符串。
 */
function safeDate(value) {
  const text = typeof value === 'string' ? value : ''
  return text.length <= 40 && Number.isFinite(Date.parse(text)) ? text : ''
}

/**
 * 将两种上游结果转换为同一受限视图，Claude 的可用数以可兑换次数为准。
 * @param platform 已验证的 codex 或 claude 平台。
 * @param block 官方次数视图，必须包含有效的非负整数。
 * @return 不含凭据和上游原文的次数及有效期。
 */
function creditView(platform, block) {
  if (!block || !Number.isSafeInteger(block.available_count) || block.available_count < 0) throw hostedError('reset_unavailable')
  const items = (Array.isArray(block.credits) ? block.credits : []).slice(0, 100).map(item => ({
    label: typeof item.label === 'string' ? item.label.slice(0, 120) : '',
    expires_at: safeDate(item.expires_at),
    starts_at: safeDate(item.starts_at),
    resets_left: platform === 'claude' && Number.isSafeInteger(item.resets_left) ? Math.max(0, item.resets_left) : 1,
    redeemable: platform === 'codex' || item.redeemable === true,
  }))
  return { platform, available_count: block.available_count, items, fetched_at: safeDate(block.fetched_at), cooldown_until: safeDate(block.cooldown_until) }
}

/**
 * 读取实时官方状态；托管 GPT 的凭据只能由槽位内核运行时刷新。
 * @param root vm2api 的私有数据根目录。
 * @param vm 已确认归属的槽位。
 * @param platform 槽位对应的账号平台。
 * @return 受限官方次数视图；没有可靠读数时抛出固定错误。
 */
async function readCredits(root, vm, platform) {
  const result = platform === 'claude'
    ? await queryClaudeResetCredits({ projectRoot: root, vmId: vm.id })
    : await queryOpenaiQuota({ projectRoot: root, vmId: vm.id, rotate: false })
  if (!result?.ok) {
    if (['CLAUDE_RESET_OAUTH_REQUIRED', 'CLAUDE_RESET_PROFILE_SCOPE_REQUIRED'].includes(result?.error)) throw hostedError('reset_unsupported')
    throw hostedError('reset_unavailable')
  }
  // GPT 的通用额度解析器可能以默认 0 表示缺失字段，只有明确读数才能展示或用于消费预检。
  if (platform === 'codex' && result.reset_credit_verified !== true) throw hostedError('reset_unavailable')
  return creditView(platform, platform === 'claude' ? result.claude_reset_credits : result.reset_credits)
}

/**
 * 查询或使用当前绑定的官方重置次数；上游结果不明时绝不声称失败或自动重试。
 * @param options 包含已核验的根目录、槽位、固定动作和操作编号。
 * @return 受限次数视图或消费结果。
 */
export async function hostedResetCredits({ root, vm, action, body }) {
  const platform = isClaudeVm(vm) ? 'claude' : isCodexVm(vm) ? 'codex' : ''
  if (!platform || body.platform !== platform) throw hostedError()
  if (action === 'reset-query') return readCredits(root, vm, platform)
  if (action !== 'reset-use' || !UUID.test(body.operation_id || '')) throw hostedError()
  let before
  try {
    before = await readCredits(root, vm, platform)
  } catch {
    // 预检未触发消费，门户可以解除本次未确认保护。
    return { outcome: 'not_used', reason: 'query_unavailable' }
  }
  if (before.available_count <= 0) return { outcome: 'not_used', reason: 'not_available' }
  try {
    const result = platform === 'claude'
      ? await redeemClaudeResetCredit({ projectRoot: root, vmId: vm.id, idempotencyKey: body.operation_id })
      : await resetOpenaiQuota({ projectRoot: root, vmId: vm.id, rotate: false })
    if (!result?.ok) return { outcome: 'unknown' }
    if (platform === 'codex' || result.outcome === 'reset') return { outcome: 'reset' }
    if (['not_limited', 'cooldown', 'ineligible'].includes(result.outcome)) return { outcome: 'not_used', reason: result.outcome }
    return { outcome: 'unknown' }
  } catch {
    // 请求可能已到达官方服务；让门户保留持久化的未确认状态。
    return { outcome: 'unknown' }
  }
}
