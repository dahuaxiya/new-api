/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import type { ComponentType } from 'react'

import type { ModelDetailsContentProps } from './model-details'

let pending: Promise<ComponentType<ModelDetailsContentProps>> | undefined

/**
 * 按需读取详情模块，并合并预取和打开抽屉的并发加载。失败后释放缓存以允许重试。
 * @return 原有详情内容组件；价格和接口展示逻辑继续由该组件处理。
 */
export function loadModelDetailsContent() {
  pending ??= import('./model-details')
    .then((module) => module.ModelDetailsContent)
    .catch((error: unknown) => {
      // 失败的 Promise 不能永久缓存，否则用户重试只会得到同一个拒绝结果。
      pending = undefined
      throw error
    })
  return pending
}
