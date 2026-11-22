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
export type TextQuote = { id: string; text: string; note: string }
export type ImagePin = { id: string; x: number; y: number; note: string }

/** 以原图左上角为原点。提示词携带百分比坐标，避免向上游发送额外一份标注图。 */
export function annotatedPrompt(
  draft: string,
  quotes: TextQuote[],
  pins: ImagePin[],
  imageNote: string
) {
  return [
    ...quotes.map(({ text, note }) =>
      [text.trim().replaceAll(/^/gm, '> '), note.trim()]
        .filter(Boolean)
        .join('\n')
    ),
    imageNote.trim(),
    ...pins.map(
      ({ x, y, note }, i) =>
        `Point #${i + 1} (x=${x}%, y=${y}%, origin: top-left): ${note.trim()}`
    ),
    draft.trim(),
  ]
    .filter(Boolean)
    .join('\n\n')
}
