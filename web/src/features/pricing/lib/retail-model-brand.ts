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
const brands = [
  [/^(gpt-|chatgpt-|o[134](?:-|$)|codex-)/i, 'OpenAI', 'OpenAI'],
  [/^claude-/i, 'Anthropic', 'Claude.Color'],
  [/^(gemini-|gemma-|imagen-|veo-)/i, 'Google', 'Gemini.Color'],
  [/^grok-/i, 'XAI', 'Grok'],
  [/^deepseek/i, 'DeepSeek.Color', 'DeepSeek.Color'],
  [/^(qwen|qwq|qvq)/i, 'Qwen.Color', 'Qwen.Color'],
  [/^(kimi|moonshot)/i, 'Moonshot', 'Kimi.Color'],
  [/^(glm|z-ai)/i, 'Zhipu.Color', 'Zhipu.Color'],
] as const

/** Resolve a display brand from the canonical model name carried by the price book.
 * @param model Canonical model name, independent of the selected supplier route.
 * @return Company and model icon names, or undefined for an unrecognized brand.
 */
export function retailModelBrand(model: string) {
  return brands.find(([pattern]) => pattern.test(model))
}
