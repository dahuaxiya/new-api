import { Link } from '@tanstack/react-router'
import type { ReactNode } from 'react'

import { isIssuerFrame, retailOrigin } from '../lib/embedded-login'

/** 内嵌身份表单的普通点击由中转站路由接管。复制链接或在新标签打开时也使用中转站地址。 */
export function AuthRouteLink({
  to,
  className,
  children,
}: {
  to: '/sign-in' | '/sign-up'
  className: string
  children: ReactNode
}) {
  if (!isIssuerFrame()) {
    return (
      <Link to={to} className={className}>
        {children}
      </Link>
    )
  }
  return (
    <a
      href={retailOrigin + to}
      target='_top'
      className={className}
      onClick={(event) => {
        if (
          event.defaultPrevented ||
          event.button !== 0 ||
          event.ctrlKey ||
          event.metaKey ||
          event.altKey ||
          event.shiftKey
        ) {
          return
        }
        event.preventDefault()
        window.parent.postMessage(
          {
            type:
              to === '/sign-up' ? 'luobo.form.sign-up' : 'luobo.form.sign-in',
          },
          retailOrigin
        )
      }}
    >
      {children}
    </a>
  )
}
