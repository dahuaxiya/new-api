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
import { useEffect, useRef, useState, type ComponentType } from 'react'
import { useTranslation } from 'react-i18next'

import { sideDrawerContentClassName } from '@/components/drawer-layout'
import { ErrorState } from '@/components/error-state'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'

import { loadModelDetailsContent } from './load-model-details'
import type { ModelDetailsContentProps } from './model-details'

export interface ModelDetailsDrawerProps extends ModelDetailsContentProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

/**
 * 立即打开原有详情抽屉，再按需加载内容。关闭后忽略迟到结果，加载失败仅影响抽屉。
 * @param props 原有模型和价格参数；关闭行为由调用方管理，不能因加载完成自行重开。
 * @return 始终可关闭、可重试的详情抽屉，保留 Sheet 的焦点和键盘行为。
 */
export function ModelDetailsDrawer(props: ModelDetailsDrawerProps) {
  const { t } = useTranslation()
  const [Content, setContent] =
    useState<ComponentType<ModelDetailsContentProps> | null>(null)
  const [failed, setFailed] = useState(false)
  const [attempt, setAttempt] = useState(0)
  const contentRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!props.open || Content) return
    let active = true
    setFailed(false)
    void loadModelDetailsContent().then(
      (component) => {
        if (active) setContent(() => component)
      },
      () => {
        if (active) setFailed(true)
      }
    )
    // import 无法取消，但关闭或重试后的旧结果不能改变当前抽屉状态。
    return () => {
      active = false
    }
  }, [props.open, Content, attempt])

  return (
    <Sheet open={props.open} onOpenChange={props.onOpenChange}>
      <SheetContent
        side='right'
        className={sideDrawerContentClassName(
          'model-details-drawer sm:max-w-2xl lg:max-w-3xl xl:max-w-4xl 2xl:max-w-5xl'
        )}
      >
        <SheetHeader className='sr-only'>
          <SheetTitle>{props.model.model_name}</SheetTitle>
          <SheetDescription>{t('Model details')}</SheetDescription>
        </SheetHeader>
        <div
          ref={contentRef}
          tabIndex={-1}
          role='region'
          aria-label={t('Model details')}
          className='flex-1 overflow-y-auto px-4 pt-11 pb-5 sm:px-6 sm:pt-12 sm:pb-6'
        >
          {Content ? (
            <Content {...props} />
          ) : (
            <>
              <h2 className='mb-6 font-mono text-xl break-all'>
                {props.model.model_name}
              </h2>
              {failed ? (
                <div role='alert'>
                  <ErrorState
                    title={t('Model details could not be loaded.')}
                    description={t('If this keeps failing, refresh the page.')}
                    onRetry={() => {
                      // 重试会卸载错误按钮，先把焦点移到始终保留的内容区。
                      contentRef.current?.focus()
                      setAttempt((value) => value + 1)
                    }}
                  />
                </div>
              ) : (
                <div role='status' aria-busy='true' className='space-y-5'>
                  <span className='sr-only'>
                    {t('Loading model details...')}
                  </span>
                  <Skeleton className='h-10 w-full' />
                  <Skeleton className='h-48 w-full' />
                  <Skeleton className='h-32 w-full' />
                </div>
              )}
            </>
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}
