/** 使用临时 SQLite/槽位文件验证排他权限和清理；Docker/OAuth 是唯一被替换的外部边界。 */
import { test, mock, after } from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { pathToFileURL } from 'node:url'
const root = process.env.HOSTING_VM2API_ROOT
if (!root) throw new Error('HOSTING_VM2API_ROOT must point to a patched vm2api source tree')
const moduleURL = file => pathToFileURL(path.join(root,'lib',file)).href
const runtime = await import(moduleURL('vm/slot-runtime.mjs'))
let stopOK = true
mock.module(moduleURL('vm/slot-runtime.mjs'), { namedExports:{...runtime, stopSlot:async()=>({ok:stopOK}),startSlotReady:async()=>({ok:true})} })
const identity = await import(moduleURL('admin/hosted-identity.mjs'))
let identityReadHook = null
const official = {account_hash:'a'.repeat(64),subject_hash:'b'.repeat(64),label:'a***@example.test',plan:'plus'}
mock.module(moduleURL('admin/hosted-identity.mjs'), {namedExports:{...identity,verifyHostedIdentity:async()=>{if(identityReadHook) await identityReadHook();return official},probeHostedExit:async()=>{}}})
const oauth = await import(moduleURL('oauth/codex-oauth.mjs'))
mock.module(moduleURL('oauth/codex-oauth.mjs'), {namedExports:{...oauth,exchangeCodexAuthCode:async()=>({access_token:'fixture-access',refresh_token:'fixture-refresh',id_token:'fixture-id'})}})
const { handleHostedPanel } = await import(moduleURL('admin/hosted-panel.mjs'))
const { openDatabase, closeDatabase } = await import(moduleURL('db/database.mjs'))
const { hostedDb, hostedRecord, hostedAllows, withHostedWrite, assertHostedWrite } = await import(moduleURL('admin/hosted-store.mjs'))
const { readCodexAccounts, writeCodexAccounts } = await import(moduleURL('vm/codex-slot.mjs'))
const { ApiKeyStore } = await import(moduleURL('admin/api-keys.mjs'))
const { keyAllowsVm } = await import(moduleURL('admin/key-scope.mjs'))
const { getVm } = await import(moduleURL('vm/vm-registry.mjs'))
const tmp = fs.mkdtempSync(path.join(os.tmpdir(),'hosting-lifecycle-'))
process.env.VM2API_DB_SECRET = 'local-hosting-test-secret-only'
openDatabase({dbPath:path.join(tmp,'hosting.db')})
fs.mkdirSync(path.join(tmp,'vms'),{recursive:true})
const vm = { id:'fixture-slot',name:'Fixture',platform:'openai',family:'codex',codex_kernel:true,runtime:{type:'docker'},proxy:{id:'px-local'},device_id:'fixture-device' }
fs.writeFileSync(path.join(tmp,'vms',vm.id+'.json'),JSON.stringify(vm))
const keys = new ApiKeyStore({db:hostedDb()})
const ctx = {cfg:{paths:{project:tmp}},routingConfig:{},apiKeyStore:keys,requireSlotProxy:()=>({ok:true,proxyUrl:''}),readBody:async req=>req.body,json:(res,status,body)=>{res.status=status;res.body=body}}
const binding = '00000000-0000-4000-8000-000000000001'
/** 经真实面板协议入口操作，只记录无敏感信息的状态断言。 */
async function call(action, body={}, role='admin') {
 const res={}
 await handleHostedPanel(ctx,{method:'POST',panelRole:role,body},res,new URL(`https://fixture/api/panel/hosting/slots/${vm.id}/${action}`),async ({account})=>writeCodexAccounts(tmp,vm.id,[account]))
 return res
}
after(()=>{closeDatabase();fs.rmSync(tmp,{recursive:true,force:true});mock.restoreAll()})

test('prepare, activate, pause, fencing and verified cleanup preserve the prebuilt slot', async()=>{
 assert.equal((await call('enroll',{},'viewer')).status,409)
 assert.equal(hostedRecord(vm.id),null)
 assert.equal((await call('enroll')).status,200)
 assert.equal(hostedAllows(null,vm.id),false,'master cannot use inventory')
 assert.equal((await call('reserve',{binding_id:binding,generation:1})).status,200)
 assert.equal((await call('reserve',{binding_id:binding.replace(/1$/,'2'),generation:2})).status,409)
 assert.throws(()=>writeCodexAccounts(tmp,vm.id,[{access_token:'forged'}]),/hosting_managed_credentials/)
 const login = await call('login',{binding_id:binding,generation:2})
 assert.equal(login.status,200)
 const auth=new URL(new URL(login.body.data.auth_url).searchParams.get('authorize_url'))
 const cb='http://localhost:1455/auth/callback?code=fixture-code&state='+auth.searchParams.get('state')
 const prepared=await call('prepare',{binding_id:binding,generation:3,session_id:login.body.data.session_id,callback_url:cb})
 assert.equal(prepared.status,200)
 assert.equal(hostedRecord(vm.id).state,'prepared')
 assert.ok(!hostedRecord(vm.id).pending.includes('fixture-access'),'pending token encrypted')
 assert.equal(readCodexAccounts(tmp,vm.id).length,0,'not activated before central identity claim')
 const now=Math.floor(Date.now()/1000)
 const body={binding_id:binding,generation:4,...official,starts_at:now-1,ends_at:now+3600}
 const active=await call('activate',body)
 assert.equal(active.status,200)
 const key=active.body.data.key_id
 assert.ok(active.body.data.key)
 assert.equal(hostedAllows({hosted_key_id:key},vm.id),true)
 assert.equal(keyAllowsVm({all:true},getVm(tmp,vm.id)),false,'master/all rejected at normal schedule boundary')
 assert.equal(hostedAllows({hosted_key_id:key},'legacy-slot'),false,'hosted key cannot access legacy slots')
 assert.equal(hostedAllows({hosted_key_id:key},vm.id,now+3600),false,'remote independently checks expiry')
 const repeated=await call('activate',{...body,generation:5})
 assert.equal(repeated.body.data.key_id,key,'retry reuses key')
 assert.equal(keys.list().length,1)
 assert.equal((await call('policy',{...body,generation:6,enabled:false})).status,200)
 assert.equal(hostedAllows({hosted_key_id:key},vm.id),false)
 assert.equal((await call('policy',{...body,generation:4,enabled:true})).status,409,'stale operation rejected')
 let pendingWrite
 await withHostedWrite(vm.id,async()=>{pendingWrite=()=>assertHostedWrite(vm.id)})
 assert.throws(pendingWrite,/hosting_managed_credentials/,'write permit is not transferable')
 stopOK=false
 assert.equal((await call('release',{binding_id:binding,generation:7})).status,409)
 assert.equal(hostedRecord(vm.id).binding_id,binding,'failed cleanup retains ownership')
 assert.equal(hostedRecord(vm.id).state,'cleaning')
 stopOK=true
 assert.equal((await call('release',{binding_id:binding,generation:8})).status,200)
 assert.equal(hostedRecord(vm.id).state,'inventory')
 assert.equal(readCodexAccounts(tmp,vm.id).length,0)
 assert.equal(getVm(tmp,vm.id).proxy.id,'px-local')
 assert.equal(getVm(tmp,vm.id).device_id,'fixture-device')
 assert.equal(hostedAllows({hosted_key_id:key},'legacy-slot'),false,'revoked keys keep their isolation tombstone')
 assert.equal((await call('release',{binding_id:binding,generation:9})).status,200,'cleanup idempotent')
 const nextBinding=binding.replace(/1$/,'2')
 assert.equal((await call('reserve',{binding_id:nextBinding,generation:1})).status,200,'new owner starts its own generation')
 assert.equal(hostedAllows({hosted_key_id:key},vm.id),false,'old key cannot access reallocated slot')
 assert.equal((await call('policy',{...body,generation:99,enabled:true})).status,409,'old owner cannot modify new binding')
})

test('legacy takeover validates scope before mutation and resumes one isolated key after response loss', async()=>{
 const originalId=vm.id
 vm.id='legacy-takeover'
 fs.writeFileSync(path.join(tmp,'vms',vm.id+'.json'),JSON.stringify(vm))
 fs.mkdirSync(path.join(tmp,'vms',vm.id),{recursive:true})
 writeCodexAccounts(tmp,vm.id,[{access_token:'legacy-access',id_token:'legacy-id',refresh_token:'legacy-refresh'}])
 const key=keys.create({name:'legacy-specific',group_type:'openai',allowed_vms:[vm.id],max_concurrency:3})
 const original=keys.repo.getById(key.id)
 const {plainApiKey}=await import(moduleURL('admin/api-keys.mjs'))
 const secret=plainApiKey(original)
 // 验签等待期间另一个管理入口更新凭据时，必须拒绝用旧身份接管新凭据。
 identityReadHook=async()=>writeCodexAccounts(tmp,vm.id,[{access_token:'changed-access',id_token:'changed-id',refresh_token:'changed-refresh'}])
 assert.equal((await call('takeover-check',{key:secret})).status,409)
 assert.equal(hostedRecord(vm.id),null)
 identityReadHook=null
 writeCodexAccounts(tmp,vm.id,[{access_token:'legacy-access',id_token:'legacy-id',refresh_token:'legacy-refresh'}])
 const checked=await call('takeover-check',{key:secret})
 assert.equal(checked.status,200)
 assert.equal(checked.body.data.state,'checked')
 assert.equal(hostedRecord(vm.id),null,'preflight must not isolate the old slot')
 const duplicate=keys.create({name:'shared-explicit',group_type:'openai',allowed_vms:[vm.id]})
 assert.equal((await call('takeover-check',{key:secret})).status,409,'shared explicit keys must block takeover')
 assert.equal(hostedRecord(vm.id),null)
 keys.update(duplicate.id,{allowed_vms:['other-slot']})
 const body={key:secret,key_id:key.id,binding_id:'00000000-0000-4000-8000-000000000003',generation:1,...official}
 assert.equal((await call('takeover',{...body,account_hash:'c'.repeat(64)})).status,409)
 assert.equal(hostedRecord(vm.id),null,'identity changes must leave legacy service alone')
 assert.equal((await call('takeover',body)).status,200)
 assert.equal(hostedAllows({group_type:'all'},vm.id),false,'permanent isolation must precede activation')
 assert.equal(readCodexAccounts(tmp,vm.id)[0].access_token,'legacy-access','takeover does not exchange or rotate credentials')
 assert.equal((await call('takeover',{...body,generation:2})).status,200,'same binding resumes after response loss')
 const now=Math.floor(Date.now()/1000)
 const active=await call('activate',{...body,generation:2,starts_at:now-1,ends_at:now+3600})
 assert.equal(active.status,200)
 assert.equal(active.body.data.key,secret,'exclusive existing upstream key is retained')
 assert.equal(active.body.data.key_id,key.id)
 assert.equal(hostedAllows({hosted_key_id:key.id},vm.id),true)
 assert.equal(hostedAllows({hosted_key_id:key.id},'other-slot'),false)
 const scheduled=await call('activate',{...body,generation:3,starts_at:now+60,ends_at:now+3600})
 assert.equal(scheduled.status,200)
 assert.equal(hostedAllows({hosted_key_id:key.id},vm.id),false,'future terms are not yet usable')
 assert.equal((await call('policy',{...body,generation:4,starts_at:now-1,ends_at:now+3600,enabled:true})).status,200)
 assert.equal(hostedAllows({hosted_key_id:key.id},vm.id),true,'policy reconciliation resumes the same key')
 assert.equal((await call('takeover',{...body,binding_id:binding,generation:99})).status,409)
 vm.id=originalId
})
