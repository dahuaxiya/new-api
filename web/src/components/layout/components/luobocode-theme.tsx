import { useEffect } from 'react'

import { isLuobocodeBrand } from '@/lib/luobocode-brand'
import { useSystemConfigStore } from '@/stores/system-config-store'

/**
 * 把本站主题放到文档层，让页面、弹窗及手机侧栏继承同一组变量。
 * 仅识别 luobocode 品牌；CSS 保留用户显式选择的主题、字体和圆角。
 * @return 不产生页面元素的主题同步器，根布局只能挂载一个实例。
 */
export function LuobocodeTheme() {
  const systemName = useSystemConfigStore((state) => state.config.systemName)
  const branded = isLuobocodeBrand(systemName)

  useEffect(() => {
    // Portal 挂在 body 下；把样式留在路由容器会导致弹窗退回默认配色。
    document.body.classList.toggle('luobo-shell', branded)
    return () => document.body.classList.remove('luobo-shell')
  }, [branded])

  return null
}
