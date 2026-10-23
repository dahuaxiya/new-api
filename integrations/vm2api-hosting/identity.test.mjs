/** 验证真实 RSA 签名及协议失败条件；只用可控 HTTP 公钥响应代替官方网络。 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import path from 'node:path'
import { pathToFileURL } from 'node:url'
const root=process.env.HOSTING_VM2API_ROOT
const {verifyHostedIdentity}=await import(pathToFileURL(path.join(root,'lib/admin/hosted-identity.mjs')))
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
test('only signed personal identity for this audience is accepted',async()=>{
 const result=await verifyHostedIdentity({id_token:token()},'',fetchOfficial)
 assert.equal(result.plan,'plus');assert.equal(result.label,'a***@example.test');assert.equal(result.account_hash.length,64)
 for(const invalid of [{...claims,exp:now-1},{...claims,iss:'https://evil.test'},{...claims,aud:'another-client'},{...claims,iat:now+3600},{...claims,sub:''},{...claims,'https://api.openai.com/auth':{chatgpt_account_id:'account',chatgpt_plan_type:'team'}}]) {
  await assert.rejects(verifyHostedIdentity({id_token:token(invalid)},'',fetchOfficial))
 }
 await assert.rejects(verifyHostedIdentity({id_token:token(claims,{alg:'none',kid:'fixture'})},'',fetchOfficial))
 const forged=token().split('.');forged[1]=Buffer.from(JSON.stringify({...claims,sub:'forged'})).toString('base64url')
 await assert.rejects(verifyHostedIdentity({id_token:forged.join('.')},'',fetchOfficial))
 await assert.rejects(verifyHostedIdentity({id_token:token()},'',async()=>({status:200,json:async()=>({issuer:claims.iss,jwks_uri:'http://127.0.0.1/secret'})})))
})
