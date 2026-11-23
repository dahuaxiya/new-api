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
const commonEmailDomains = [
  'qq.com',
  '163.com',
  'gmail.com',
  'outlook.com',
  '126.com',
  'icloud.com',
  'hotmail.com',
  'foxmail.com',
  'yeah.net',
] as const

export type EmailSuggestion = {
  address: string
  domain: string
  kind: 'completion' | 'correction'
}

/**
 * 计算用户已输入域名前缀与常见邮箱域名的近似距离。结尾尚未输入的部分
 * 不计成本，同时允许相邻字母写反，避免常见的 gmial 类错误被漏掉。
 * @param typed 用户当前输入的域名或域名前缀。
 * @param target 待比较的常见邮箱域名。
 * @return 最低编辑成本；数值越小越接近目标域名。
 */
export function emailDomainDistance(typed: string, target: string): number {
  const costs = Array.from({ length: typed.length + 1 }, (_, row) => [row])
  costs[0] = Array.from({ length: target.length + 1 }, (_, column) => column)

  for (let row = 1; row <= typed.length; row += 1) {
    for (let column = 1; column <= target.length; column += 1) {
      const sameCharacter = typed[row - 1] === target[column - 1]
      let score = Math.min(
        costs[row - 1][column] + 1,
        costs[row][column - 1] + 1,
        costs[row - 1][column - 1] + Number(!sameCharacter)
      )

      if (
        row >= 2 &&
        column >= 2 &&
        typed[row - 2] === target[column - 1] &&
        typed[row - 1] === target[column - 2]
      ) {
        score = Math.min(score, costs[row - 2][column - 2] + 1)
      }
      costs[row][column] = score
    }
  }

  return Math.min(...costs[typed.length].slice(1))
}

/**
 * 根据当前邮箱输入生成补全或纠错建议。已经有 @ 后才开始推荐域名；
 * 完整且被识别的域名不再展示菜单，未知域名仍允许用户自行提交。
 * @param value 邮箱输入框当前值。
 * @return 最多四条域名补全，或两条高置信度的拼写修正。
 */
export function getEmailSuggestions(value: string): EmailSuggestion[] {
  const trimmed = value.trim()
  const [localPart = '', domainPrefix = '', ...extraParts] = trimmed.split('@')
  if (
    !localPart ||
    extraParts.length > 0 ||
    /\s/.test(localPart) ||
    !/^[a-z0-9.-]*$/i.test(domainPrefix)
  ) {
    return []
  }

  const typedDomain = domainPrefix.toLowerCase()
  if (
    commonEmailDomains.includes(
      typedDomain as (typeof commonEmailDomains)[number]
    )
  ) {
    return []
  }

  const completions = commonEmailDomains.filter((domain) =>
    domain.startsWith(typedDomain)
  )
  if (completions.length > 0) {
    return completions.slice(0, 4).map((domain) => ({
      address: `${localPart}@${domain}`,
      domain,
      kind: 'completion',
    }))
  }

  if (
    typedDomain.length < 2 ||
    typedDomain.length > 14 ||
    !/^[a-z0-9.]+$/.test(typedDomain)
  ) {
    return []
  }

  const correctionBudget = typedDomain.length >= 4 ? 2 : 1
  const candidates = commonEmailDomains
    .filter(
      (domain) => domain.startsWith(typedDomain[0]) || typedDomain.length >= 4
    )
    .map((domain) => ({
      domain,
      score: emailDomainDistance(
        typedDomain,
        typedDomain.includes('.') ? domain : domain.split('.')[0]
      ),
    }))
    .filter(
      (candidate) =>
        candidate.score <= correctionBudget &&
        (candidate.domain.startsWith(typedDomain[0]) || candidate.score <= 1)
    )
    .sort((left, right) => left.score - right.score)

  const bestScore = candidates[0]?.score
  if (bestScore === undefined) return []

  return candidates
    .filter((candidate) => candidate.score === bestScore)
    .slice(0, 2)
    .map((candidate) => ({
      address: `${localPart}@${candidate.domain}`,
      domain: candidate.domain,
      kind: 'correction',
    }))
}
