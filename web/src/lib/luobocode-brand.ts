/**
 * 识别内置品牌以及两站的展示后缀，避免站名加“· 中转站”后丢失共用外观。
 * @param name 后台展示名称；其他品牌名称不自动套用 luobocode 主题。
 * @return 名称是否属于 luobocode 品牌。
 */
export function isLuobocodeBrand(name: string): boolean {
  return /^luobocode(?:\s*·.*)?$/i.test(name.trim())
}
