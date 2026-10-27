/** 验证真实 RSA 签名及协议失败条件；只用可控 HTTP 公钥响应代替官方网络。 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import path from 'node:path'
import { pathToFileURL } from 'node:url'
const root=process.env.HOSTING_VM2API_ROOT
const {verifyHostedIdentity,probeHostedExit}=await import(pathToFileURL(path.join(root,'lib/admin/hosted-identity.mjs')))
const {CODEX_OAUTH_CLIENT_ID}=await import(pathToFileURL(path.join(root,'lib/protocol/codex-models.mjs')))
const pair=crypto.generateKeyPairSync('rsa',{modulusLength:2048})
const jwk={...pair.publicKey.export({format:'jwk'}),kid:'fixture',alg:'RS256',use:'sig'}
const now=Math.floor(Date.now()/1000)
const claims={iss:'https://auth.openai.com',aud:CODEX_OAUTH_CLIENT_ID,iat:now,exp:now+3600,sub:'subject',email:'alice@example.test','https://api.openai.com/auth':{chatgpt_account_id:'account',chatgpt_plan_type:'plus'}}
/** 测试签发器只生成独立测试密钥的 JWT，不读取或持久化真实账号授权。 */
function token(payload=claims,header={alg:'RS256',kid:'fixture'}) {
 const data=[header,payload].map(x=>Buffer.from(JSON.stringify(x)).toString('base64url')).join('.')
 return data+'.'+crypto.sign('RSA-SHA256',Buffer.from(data),pair.privateKey).toString('base64url')
}
/** 固定站点及禁止重定向都由真实验证器检查。 */
async function fetchOfficial(url,opts) {
 assert.equal(opts.redirect,'error')
 return {status:200,json:async()=>url.endsWith('openid-configuration')?{issuer:'https://auth.openai.com',jwks_uri:'https://auth.openai.com/.well-known/jwks.json'}:{keys:[jwk]}}
}
/** 放开套餐后仍必须拒绝失效、伪造、错受众或缺少主体/账号的授权，防止准入变更绕过身份验证。 */
test('only signed account identity for this audience is accepted',async()=>{
 const result=await verifyHostedIdentity({id_token:token()},'',fetchOfficial)
 assert.equal(result.plan,'plus');assert.equal(result.label,'a***@example.test');assert.equal(result.account_hash.length,64)
 for(const invalid of [{...claims,exp:now-1},{...claims,iss:'https://evil.test'},{...claims,aud:'another-client'},{...claims,iat:now+3600},{...claims,nbf:now+3600},{...claims,azp:'another-client'},{...claims,sub:''},{...claims,'https://api.openai.com/auth':{chatgpt_plan_type:'pro_500'}}]) {
  await assert.rejects(verifyHostedIdentity({id_token:token(invalid)},'',fetchOfficial))
 }
 await assert.rejects(verifyHostedIdentity({id_token:token(claims,{alg:'none',kid:'fixture'})},'',fetchOfficial))
 const forged=token().split('.');forged[1]=Buffer.from(JSON.stringify({...claims,sub:'forged'})).toString('base64url')
 await assert.rejects(verifyHostedIdentity({id_token:forged.join('.')},'',fetchOfficial))
 await assert.rejects(verifyHostedIdentity({id_token:token()},'',async()=>({status:200,json:async()=>({issuer:claims.iss,jwks_uri:'http://127.0.0.1/secret'})})))
})

/** 官方签名与身份不变时，新套餐、无套餐或异常展示值都不得阻断绑定，也不得改变账号归属。 */
test('subscription labels are optional bounded metadata, not an admission filter',async()=>{
 const baseline=await verifyHostedIdentity({id_token:token()},'',fetchOfficial)
 const cases=[['pro_500','pro_500'],['pro500','pro500'],['team','team'],['future-tier','future-tier'],[undefined,'unknown'],[null,'unknown'],['','unknown'],[{},'unknown'],['x'.repeat(80),'x'.repeat(32)]]
 for(const [raw,expected] of cases) {
  const payload={...claims,'https://api.openai.com/auth':{chatgpt_account_id:'account',chatgpt_plan_type:raw}}
  const result=await verifyHostedIdentity({id_token:token(payload)},'',fetchOfficial)
  assert.equal(result.plan,expected)
  assert.equal(result.account_hash,baseline.account_hash)
  assert.equal(result.subject_hash,baseline.subject_hash)
 }
})

const {verifyHostedClaudeIdentity}=await import(pathToFileURL(path.join(root,'lib/admin/hosted-claude.mjs')).href)
test('Claude identity requires official bootstrap, full scope and the same account',async()=>{
 const account='00000000-0000-4000-8000-000000000010', org='00000000-0000-4000-8000-000000000011'
 const tokens={access_token:'fixture-access',refresh_token:'fixture-refresh',scope:'user:profile user:inference',account_uuid:account}
 const fetchOfficial=async(url,opts)=>{
  assert.equal(new URL(url).hostname,'api.anthropic.com');assert.equal(opts.redirect,'error')
  return {ok:true,json:async()=>({account:{uuid:account,email_address:'owner@example.test',has_claude_max:true},organization:{uuid:org}})}
 }
 const result=await verifyHostedClaudeIdentity(tokens,'',fetchOfficial)
 assert.equal(result.identity.plan,'max');assert.equal(result.identity.label,'o***@example.test')
 assert.equal(result.tokens.account_uuid,account)
 await assert.rejects(verifyHostedClaudeIdentity({...tokens,refresh_token:''},'',fetchOfficial))
 await assert.rejects(verifyHostedClaudeIdentity({...tokens,account_uuid:org},'',fetchOfficial))
 await assert.rejects(verifyHostedClaudeIdentity(tokens,'',async()=>({ok:false,status:401})))
 await assert.rejects(verifyHostedClaudeIdentity(tokens,'',async()=>({ok:true,json:async()=>({account:{uuid:account},organization:{uuid:org}})})))
})

/** 每个平台探测自身固定官方站点，不把另一平台连通当作可用。 */
test('Claude egress requires the official unauthenticated response',async()=>{
 await probeHostedExit('', 'claude', async(url,opts)=>{assert.equal(new URL(url).hostname,'api.anthropic.com');assert.equal(opts.redirect,'error');return {status:401}})
 await assert.rejects(probeHostedExit('', 'claude', async()=>({status:403})))
})
