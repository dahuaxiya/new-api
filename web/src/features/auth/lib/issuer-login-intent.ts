const restartKey = 'luobo.issuer-login-restart.v1'

/** 向固定消费站的同标签页窗口核对一次性事务；忽略其他窗口和来源的消息，超时重新发起登录。 */
export function consumeIssuerLogin(
  client: string,
  state: string,
  challenge: string
): Promise<boolean> {
  return new Promise((resolve) => {
    const frame = document.createElement('iframe')
    frame.hidden = true
    frame.title = 'SSO'
    const finish = (matched: boolean) => {
      clearTimeout(timer)
      window.removeEventListener('message', receive)
      frame.remove()
      resolve(matched)
    }
    const receive = (event: MessageEvent) => {
      if (
        event.origin !== client ||
        event.source !== frame.contentWindow ||
        event.data?.type !== 'luobo.login.checked' ||
        event.data?.state !== state
      )
        return
      if (event.data.matched === true) {
        try {
          sessionStorage.removeItem(restartKey)
        } catch {
          /* 不影响已核对的事务。 */
        }
      }
      finish(event.data.matched === true)
    }
    const timer = setTimeout(() => finish(false), 5000)
    window.addEventListener('message', receive)
    frame.onload = () =>
      frame.contentWindow?.postMessage(
        { type: 'luobo.login.check', state, challenge },
        client
      )
    frame.src = new URL('/brand/site-login-check.html', client).href
    document.body.append(frame)
  })
}

/** 遗留链接回到消费站初始化新事务；浏览器禁用跨站存储时限制自动重试次数。 */
export function mayRestartIssuerLogin(): boolean {
  try {
    if (Number(sessionStorage.getItem(restartKey)) > Date.now()) return false
    sessionStorage.setItem(restartKey, String(Date.now() + 60_000))
    return true
  } catch {
    return false
  }
}
