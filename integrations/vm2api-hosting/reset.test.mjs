/** 仅替换官方网络边界，验证两种账号的可用次数、预检和不确定结果。 */
import { test, mock, after } from 'node:test'
import assert from 'node:assert/strict'
import path from 'node:path'
import { pathToFileURL } from 'node:url'

const root = process.env.HOSTING_VM2API_ROOT
if (!root) throw new Error('HOSTING_VM2API_ROOT must point to a patched vm2api source tree')
const moduleURL = file => pathToFileURL(path.join(root, 'lib', file)).href
let gptReading, claudeReading, gptResult, claudeResult, uses = 0
mock.module(moduleURL('oauth/openai-quota.mjs'), { namedExports: {
  queryOpenaiQuota: async ({ rotate }) => { assert.equal(rotate, false); return gptReading },
  resetOpenaiQuota: async ({ rotate }) => { assert.equal(rotate, false); uses++; return gptResult },
} })
mock.module(moduleURL('oauth/claude-reset-credits.mjs'), { namedExports: {
  queryClaudeResetCredits: async () => claudeReading,
  redeemClaudeResetCredit: async ({ idempotencyKey }) => { assert.match(idempotencyKey, /^[0-9a-f-]{36}$/); uses++; return claudeResult },
} })
const { hostedResetCredits } = await import(moduleURL('admin/hosted-reset.mjs'))
after(() => mock.restoreAll())

test('GPT 缺少有效读数时不把默认零次当作结果，也不消费', async () => {
  const vm = { id: 'fixture', platform: 'openai' }
  const body = { platform: 'codex', operation_id: '00000000-0000-4000-8000-000000000001' }
  gptReading = { ok: true, reset_credits: { available_count: 0, credits: [] } }
  await assert.rejects(hostedResetCredits({ root, vm, action: 'reset-query', body }), { code: 'reset_unavailable' })
  assert.equal((await hostedResetCredits({ root, vm, action: 'reset-use', body })).outcome, 'not_used')
  assert.equal(uses, 0)
  gptReading = { ok: true, reset_credit_verified: true, reset_credits: { available_count: 1, credits: [{ expires_at: '2100-01-01T00:00:00Z', secret: 'not-returned' }] } }
  const view = await hostedResetCredits({ root, vm, action: 'reset-query', body })
  assert.equal(view.available_count, 1)
  assert.equal(JSON.stringify(view).includes('not-returned'), false)
  gptResult = { ok: true }
  assert.equal((await hostedResetCredits({ root, vm, action: 'reset-use', body })).outcome, 'reset')
  gptResult = { ok: false }
  assert.equal((await hostedResetCredits({ root, vm, action: 'reset-use', body })).outcome, 'unknown')
})

test('Claude 消费使用操作编号，明确拒绝和未知结果分别处理', async () => {
  const vm = { id: 'fixture', platform: 'anthropic' }
  const body = { platform: 'claude', operation_id: '00000000-0000-4000-8000-000000000002' }
  claudeReading = { ok: true, claude_reset_credits: { available_count: 2, credits: [{ label: 'fixture', resets_left: 2, redeemable: true }] } }
  assert.equal((await hostedResetCredits({ root, vm, action: 'reset-query', body })).available_count, 2)
  claudeResult = { ok: true, outcome: 'cooldown' }
  assert.deepEqual(await hostedResetCredits({ root, vm, action: 'reset-use', body }), { outcome: 'not_used', reason: 'cooldown' })
  claudeResult = { ok: true, outcome: 'unknown' }
  assert.equal((await hostedResetCredits({ root, vm, action: 'reset-use', body })).outcome, 'unknown')
  claudeResult = { ok: true, outcome: 'reset' }
  assert.equal((await hostedResetCredits({ root, vm, action: 'reset-use', body })).outcome, 'reset')
  await assert.rejects(hostedResetCredits({ root, vm, action: 'reset-query', body: { platform: 'codex' } }))
})
