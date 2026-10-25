import { useTranslation } from 'react-i18next'

import { CodeBlockFrame } from '@/components/ai-elements/code-block'
import { CopyButton } from '@/components/copy-button'

/**
 * 使用站内代码框与复制按钮展示只读示例，长行只在代码框内横向滚动。
 * @param props 示例标题及原样复制的内容，不接收用户密钥。
 * @return 紧凑且可复制的文档代码框。
 */
export function Example(props: { title: string; value: string }) {
  const { t } = useTranslation()
  return (
    <CodeBlockFrame
      className='min-w-0 shadow-none'
      showToolbar
      title={props.title}
      endActions={
        <CopyButton
          value={props.value}
          size='sm'
          aria-label={t('Copy {{file}}', { file: props.title })}
        >
          {t('Copy')}
        </CopyButton>
      }
    >
      <pre className='overflow-x-auto p-4 text-xs leading-relaxed sm:text-[13px]'>
        <code>{props.value}</code>
      </pre>
    </CodeBlockFrame>
  )
}
