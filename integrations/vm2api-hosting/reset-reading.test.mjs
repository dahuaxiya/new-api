/** 验证缺失或畸形的 GPT 官方响应不能被当成真实的零次。 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import path from 'node:path'
import { pathToFileURL } from 'node:url'

const root = process.env.HOSTING_VM2API_ROOT
if (!root) throw new Error('HOSTING_VM2API_ROOT must point to a patched vm2api source tree')
const { hasResetCreditReading } = await import(pathToFileURL(path.join(root, 'lib/oauth/openai-quota.mjs')).href)

test('只接受明确的非负整数次数或有效列表', () => {
  for (const value of [null, {}, { available_count: null }, { available_count: true }, { available_count: -1 }, { available_count: 1.2 }, { credits: ['bad'] }, 'not-json']) {
    assert.equal(hasResetCreditReading(value), false)
  }
  for (const value of [[], { credits: [] }, { available_count: 0 }, { availableCount: '2' }, [{ status: 'available' }], '{"items":[]}']) {
    assert.equal(hasResetCreditReading(value), true)
  }
})
