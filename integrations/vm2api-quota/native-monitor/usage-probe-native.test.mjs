import test from 'node:test'
import assert from 'node:assert/strict'
import { isCodexUsageProbeDue, createUsageProbeMonitor, isUsageProbeDue } from './lib/oauth/usage-probe-monitor.mjs'
const epoch = Date.parse('2026-10-21T10:00:00Z')
/** Build a credential-present GPT slot without using real credentials. */
function slot(at = epoch) {
  return { id: 'gpt-test', platform: 'openai', status: 'running', has_token: true,
    proxy: { host: '127.0.0.1', port: 1080 },
    codex_usage: { snapshot: { updated_at: new Date(at).toISOString() }, windows: [] } }
}
test('GPT freshness handles absent/future samples and refreshes at five minutes', () => {
  assert.equal(isCodexUsageProbeDue(slot(), { now: epoch + 299999 }).due, false)
  assert.equal(isCodexUsageProbeDue(slot(), { now: epoch + 300000 }).due, true)
  assert.equal(isCodexUsageProbeDue({}, { now: epoch }).due, true)
  assert.equal(isCodexUsageProbeDue(slot(epoch + 1000), { now: epoch }).due, true)
  const vm = slot(); vm.codex_usage.windows = [{ reset_at: new Date(epoch + 1000).toISOString() }]
  assert.equal(isCodexUsageProbeDue(vm, { now: epoch + 2000 }).reason, 'codex_window_elapsed')
  vm.codex_usage.snapshot.updated_at = new Date(epoch + 1500).toISOString()
  assert.equal(isCodexUsageProbeDue(vm, { now: epoch + 2000 }).due, false)
})
test('periodic and event probes share single flight and failures back off without erasing quota', async () => {
  let now = epoch + 300000, calls = 0, release
  const vm = slot()
  const monitor = createUsageProbeMonitor({ now: () => now, config: { enabled: false },
    listTargets: () => [vm, { ...slot(), id: 'stopped', status: 'stopped' }],
    probeOne: () => { calls++; return new Promise(resolve => { release = resolve }) } })
  assert.equal(monitor.start({ immediate: true }).started, false)
  const run = monitor.runOnce(); await Promise.resolve()
  const event = monitor.probeNow(vm.id)
  release({ ok: false }); await Promise.all([run, event])
  assert.equal(calls, 1)
  await monitor.runOnce(); assert.equal(calls, 1)
  assert.equal(vm.codex_usage.snapshot.updated_at, new Date(epoch).toISOString())
  now += 60000
  const retry = monitor.runOnce(); await Promise.resolve(); release({ ok: true }); await retry
  assert.equal(calls, 2)
})
test('fresh idle GPT skips while missing GPT and legacy Claude retain separate decisions', async () => {
  const fresh = slot(), missing = { ...slot(), id: 'missing', codex_usage: null }
  const calls = []
  const m = createUsageProbeMonitor({ now: () => epoch, listTargets: () => [fresh, missing],
    isActive: () => false, probeOne: vm => { calls.push(vm.id); return { ok: true } } })
  await m.runOnce(); assert.deepEqual(calls, ['missing'])
  assert.equal(isUsageProbeDue({}, { now: epoch }).reason, 'never_sampled_official')
  assert.equal(isUsageProbeDue({ last_probe: { at: new Date(epoch).toISOString() } }, { now: epoch }).due, false)
})
