/** 使用临时 SQLite/槽位文件验证排他权限和清理；Docker/OAuth 是唯一被替换的外部边界。 */
import { test, mock, after } from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import crypto from 'node:crypto'
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
const official = {account_hash:crypto.createHash('sha256').update('openai-account\0fixture-account').digest('hex'),subject_hash:crypto.createHash('sha256').update('openai-subject\0fixture-subject').digest('hex'),label:'a***@example.test',plan:'plus'}
mock.module(moduleURL('admin/hosted-identity.mjs'), {namedExports:{...identity,verifyHostedIdentity:async()=>{if(identityReadHook) await identityReadHook();return official},probeHostedExit:async()=>{}}})
const oauth = await import(moduleURL('oauth/codex-oauth.mjs'))
mock.module(moduleURL('oauth/codex-oauth.mjs'), {namedExports:{...oauth,exchangeCodexAuthCode:async()=>({access_token:'fixture-access',refresh_token:'fixture-refresh',id_token:'fixture-id'})}})
const { handleHostedPanel } = await import(moduleURL('admin/hosted-panel.mjs'))
const { openDatabase, closeDatabase, getDb } = await import(moduleURL('db/database.mjs'))
const { hostedDb, hostedRecord, hostedAllows, withHostedWrite, assertHostedWrite } = await import(moduleURL('admin/hosted-store.mjs'))
const { readCodexAccounts, writeCodexAccounts } = await import(moduleURL('vm/codex-slot.mjs'))
const { ApiKeyStore } = await import(moduleURL('admin/api-keys.mjs'))
const { VmPoolsRepo } = await import(moduleURL('db/repos/vm-pools-repo.mjs'))
const { keyAllowsVm } = await import(moduleURL('admin/key-scope.mjs'))
const { getVm } = await import(moduleURL('vm/vm-registry.mjs'))
const tmp = fs.mkdtempSync(path.join(os.tmpdir(),'hosting-lifecycle-'))
process.env.VM2API_DB_SECRET = 'local-hosting-test-secret-only'
openDatabase({dbPath:path.join(tmp,'hosting.db')})
// 以已上线旧表启动，确认新增来源列能原位升级，原有隔离逻辑继续生效。
getDb().exec(`CREATE TABLE hosted_slots (vm_id TEXT PRIMARY KEY,binding_id TEXT,generation INTEGER NOT NULL DEFAULT 0,state TEXT NOT NULL DEFAULT 'inventory',account_hash TEXT,subject_hash TEXT,key_id TEXT,pool_id TEXT,starts_at INTEGER NOT NULL DEFAULT 0,ends_at INTEGER NOT NULL DEFAULT 0,pending TEXT,label TEXT,plan TEXT)`)

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

test('root can set legacy terms with an expired identity token while reauthentication still requires official verification', async()=>{
 const originalId=vm.id
 vm.id='legacy-takeover'
 fs.writeFileSync(path.join(tmp,'vms',vm.id+'.json'),JSON.stringify(vm))
 fs.mkdirSync(path.join(tmp,'vms',vm.id),{recursive:true})
 // 旧凭据只提供已存在绑定的标识；管理员期限设置不应调用官方验签或旋转授权。
 const expiredClaims={sub:'fixture-subject',email:'alice@example.test',exp:1,'https://api.openai.com/auth':{chatgpt_account_id:'fixture-account',chatgpt_plan_type:'plus'}}
 const expiredToken=Buffer.from('{}').toString('base64url')+'.'+Buffer.from(JSON.stringify(expiredClaims)).toString('base64url')+'.fixture-signature'
 const originalAccount={access_token:'legacy-access',id_token:expiredToken,refresh_token:'legacy-refresh'}
 writeCodexAccounts(tmp,vm.id,[originalAccount])
 const originalRecords=readCodexAccounts(tmp,vm.id)
 // 线上旧 Key 使用 all + 单槽位池；池优先约束范围，不能把 all 误当成全局 Key。
 const pools=new VmPoolsRepo(hostedDb())
 const pool=pools.insert({name:'legacy-specific'})
 pools.setMembers(pool.id,[vm.id])
 const key=keys.create({name:'legacy-specific',group_type:'all',vm_pool_id:pool.id,max_concurrency:3})
 const original=keys.repo.getById(key.id)
 const {plainApiKey}=await import(moduleURL('admin/api-keys.mjs'))
 const secret=plainApiKey(original)
 identityReadHook=async()=>{throw Object.assign(new Error('identity_invalid'),{code:'identity_invalid'})}
 const checked=await call('takeover-check',{key:secret})
 assert.equal(checked.status,200)
 assert.equal(checked.body.data.state,'checked')
 assert.equal(checked.body.data.identity_source,'legacy_binding','existing references must not be described as officially verified')
 assert.equal(hostedRecord(vm.id),null,'preflight must not isolate the old slot')
 // 接受 all + 单槽位池不等于放开共享、停用或全局 Key。
 pools.setMembers(pool.id,[vm.id,'other-slot'])
 assert.equal((await call('takeover-check',{key:secret})).status,409,'multi-slot pool cannot be adopted')
 pools.setMembers(pool.id,[vm.id])
 pools.setEnabled(pool.id,false)
 assert.equal((await call('takeover-check',{key:secret})).status,409,'disabled pool cannot be adopted')
 pools.setEnabled(pool.id,true)
 keys.update(key.id,{vm_pool_id:null})
 assert.equal((await call('takeover-check',{key:secret})).status,409,'global all key is not an exclusive binding')
 keys.update(key.id,{vm_pool_id:pool.id})
 assert.equal((await call('takeover-check',{key:secret})).status,200)
 const duplicate=keys.create({name:'legacy-direct',group_type:'all',vm_pool_id:pool.id})
 const directBefore=keys.repo.getById(duplicate.id)
 assert.equal((await call('takeover-check',{key:secret})).status,200,'existing exclusive direct key must be preserved')
 assert.equal(hostedRecord(vm.id),null)
 keys.update(duplicate.id,{group_type:'openai',allowed_vms:[vm.id,'other-slot']})
 assert.equal((await call('takeover-check',{key:secret})).status,409,'shared multi-slot keys must not be silently narrowed')
 keys.update(duplicate.id,{group_type:'all',vm_pool_id:pool.id})
 const body={key:secret,key_id:key.id,binding_id:'00000000-0000-4000-8000-000000000003',generation:1,...official}
 // 管理员核对后若槽位已换账号，仍须拒绝把旧期限应用到新账号。
 const changedToken=Buffer.from('{}').toString('base64url')+'.'+Buffer.from(JSON.stringify({...expiredClaims,sub:'changed-subject'})).toString('base64url')+'.signature'
 writeCodexAccounts(tmp,vm.id,[{...originalAccount,id_token:changedToken}])
 assert.equal((await call('takeover',body)).status,409)
 assert.equal(hostedRecord(vm.id),null)
 writeCodexAccounts(tmp,vm.id,[originalAccount])
 assert.equal((await call('takeover',{...body,account_hash:'c'.repeat(64)})).status,409)
 assert.equal(hostedRecord(vm.id),null,'identity changes must leave legacy service alone')
 // 模拟第二把 Key 写入失败，槽位策略和兼容集合必须同时回滚。
 hostedDb().exec(`CREATE TRIGGER fail_legacy_keys BEFORE INSERT ON hosted_keys WHEN NEW.key_id = '${duplicate.id}' BEGIN SELECT RAISE(ABORT, 'fixture failure'); END`)
 assert.equal((await call('takeover',body)).status,409)
 assert.equal(hostedRecord(vm.id),null,'failed alias registration must not leave a partial takeover')
 assert.equal(hostedDb().prepare('SELECT COUNT(*) AS n FROM hosted_keys WHERE binding_id = ?').get(body.binding_id).n,0)
 hostedDb().exec('DROP TRIGGER fail_legacy_keys')
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
 assert.equal(hostedAllows({hosted_key_id:duplicate.id},vm.id),true,'existing direct key remains usable')
 assert.equal(plainApiKey(keys.repo.getById(duplicate.id)),plainApiKey(directBefore),'direct key secret is unchanged')
 assert.equal(keys.repo.getById(duplicate.id).status,directBefore.status,'direct key status is unchanged')
 assert.equal(hostedRecord(vm.id).key_id,key.id,'New API still has exactly one canonical upstream key')
 const lateKey=keys.create({name:'created-after-takeover',group_type:'all',vm_pool_id:pool.id})
 assert.equal(hostedAllows({hosted_key_id:lateKey.id},vm.id),false,'later keys cannot join the preserved set')
 assert.equal((await call('takeover',{...body,generation:2})).status,200)
 assert.equal(hostedAllows({hosted_key_id:lateKey.id},vm.id),false,'retry must not expand the preserved set')
 assert.equal(hostedAllows({hosted_key_id:key.id},vm.id,now+3600),false,'the original key is rejected at the exact deadline')
 assert.equal(hostedAllows({hosted_key_id:duplicate.id},vm.id,now+3600),false,'direct key shares the exact deadline')
 assert.deepEqual(readCodexAccounts(tmp,vm.id),originalRecords,'setting terms preserves all existing credentials')
 assert.equal(hostedAllows({hosted_key_id:key.id},'other-slot'),false)
 assert.equal(hostedAllows({hosted_key_id:duplicate.id},'other-slot'),false,'direct key cannot escape through a changed pool')
 const scheduled=await call('activate',{...body,generation:3,starts_at:now+60,ends_at:now+3600})
 assert.equal(scheduled.status,200)
 assert.equal(hostedAllows({hosted_key_id:key.id},vm.id),false,'future terms are not yet usable')
 assert.equal(hostedAllows({hosted_key_id:duplicate.id},vm.id),false,'direct key also obeys future terms')
 assert.equal((await call('policy',{...body,generation:4,starts_at:now-1,ends_at:now+3600,enabled:true})).status,200)
 assert.equal(hostedAllows({hosted_key_id:key.id},vm.id),true,'policy reconciliation resumes the same key')
 assert.equal(hostedAllows({hosted_key_id:duplicate.id},vm.id),true,'renewal resumes the same direct key')
 assert.equal((await call('takeover',{...body,binding_id:binding,generation:99})).status,409)
 const login=await call('login',{...body,generation:5})
 assert.equal(login.status,200)
 const auth=new URL(new URL(login.body.data.auth_url).searchParams.get('authorize_url'))
 const callback='http://localhost:1455/auth/callback?code=fixture&state='+auth.searchParams.get('state')
 const failed=await call('prepare',{...body,generation:6,session_id:login.body.data.session_id,callback_url:callback})
 assert.equal(failed.status,409,'new OAuth credentials still require official verification')
 assert.equal(failed.body.error.code,'identity_invalid')
 assert.deepEqual(readCodexAccounts(tmp,vm.id),originalRecords)
 identityReadHook=null
 const relogin=await call('login',{...body,generation:7})
 const nextAuth=new URL(new URL(relogin.body.data.auth_url).searchParams.get('authorize_url'))
 const nextCallback='http://localhost:1455/auth/callback?code=fixture&state='+nextAuth.searchParams.get('state')
 assert.equal((await call('prepare',{...body,generation:8,session_id:relogin.body.data.session_id,callback_url:nextCallback})).status,200)
 assert.equal((await call('activate',{...body,generation:9,starts_at:now-1,ends_at:now+3600})).status,200)
 assert.equal(hostedAllows({hosted_key_id:duplicate.id},vm.id),true,'official re-login does not discard preserved direct keys')
 // 永久权益覆盖主 Key 和已冻结的旧直连 Key；切换模式不能扩张 Key 集合。
 let generation=10
 const term={...body,starts_at:now-1,ends_at:-1,enabled:true}
 assert.equal((await call('activate',{...term,generation:generation++})).status,200)
 assert.equal(keys.repo.getById(key.id).expires_at,null,'permanent activation clears prior key expiry')
 for (const id of [key.id,duplicate.id]) assert.equal(hostedAllows({hosted_key_id:id},vm.id,4102444801),true)
 assert.equal(hostedAllows({hosted_key_id:lateKey.id},vm.id),false)
 for (const ends_at of [0,-2,undefined,now-2,4102444801]) {
   assert.equal((await call('policy',{...term,ends_at,generation:generation++})).status,409,'invalid terms fail closed')
 }
 assert.equal((await call('policy',{...term,enabled:false,generation:generation++})).status,200)
 assert.equal(hostedAllows({hosted_key_id:duplicate.id},vm.id),false,'permanent may still be disabled')
 assert.equal((await call('policy',{...term,starts_at:now+3600,generation:generation++})).status,200)
 assert.equal(hostedAllows({hosted_key_id:key.id},vm.id),false,'permanent does not bypass start time')
 assert.equal((await call('policy',{...term,ends_at:now+3600,generation:generation++})).status,200)
 assert.notEqual(keys.repo.getById(key.id).expires_at,null)
 for (const id of [key.id,duplicate.id]) assert.equal(hostedAllows({hosted_key_id:id},vm.id,now+3600),false,'dated conversion restores deadline')
 assert.equal((await call('policy',{...term,generation:generation++})).status,200)
 assert.equal(keys.repo.getById(key.id).expires_at,null,'permanent policy clears dated key expiry')
 assert.equal(plainApiKey(keys.repo.getById(key.id)),secret)
 assert.equal(keys.repo.getById(duplicate.id).expires_at,directBefore.expires_at,'preserved direct key metadata remains unchanged')
 assert.equal((await call('release',{...body,generation:generation++})).status,200)
 assert.equal(hostedAllows({hosted_key_id:duplicate.id},vm.id),false,'unbind revokes all preserved keys')
 assert.equal(hostedAllows({hosted_key_id:duplicate.id},'other-slot'),false,'released direct keys keep their tombstones')
 assert.equal((await call('reserve',{binding_id:binding,generation:1})).status,200)
 assert.equal(hostedAllows({hosted_key_id:duplicate.id},vm.id),false,'direct key cannot access the next owner')
 vm.id=originalId
})
