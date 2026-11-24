/** 仅这对已配置的第一方域名使用内嵌密码表单，其他安装仍沿用独立身份页。 */
export const retailOrigin = 'https://luobo.world'
export const issuerOrigin = 'https://host.luobo.world'
export const issuerFrameName = 'luobo-retail-password'

/** 表单仅在第一层专用 frame 中切换布局。消息仍须分别验证发送来源与窗口。 */
export function isIssuerFrame() {
  return (
    location.origin === issuerOrigin &&
    window.name === issuerFrameName &&
    window.parent !== window &&
    window.parent === window.top
  )
}
