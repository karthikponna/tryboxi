// Character-cell geometry and vector strokes for box-drawing glyphs. Drawing them as
// shapes keeps lines connected even when the font has no box-drawing characters.

export const CW = 7;
export const CH = 14;
export const BASELINE = 11;

// [0, x, y, w, h] rect · [1, cx, cy, r] disc
export type Prim = [0, number, number, number, number] | [1, number, number, number];

const R = (x: number, y: number, w: number, h: number): Prim => [0, x, y, w, h];

export const BOX: Record<number, Prim[]> = {
  0x2500: [R(0, 6.5, 7, 1)], // ─
  0x2502: [R(3, 0, 1, 14)], // │
  0x250c: [R(3, 6.5, 4, 1), R(3, 6.5, 1, 7.5)], // ┌
  0x2510: [R(0, 6.5, 4, 1), R(3, 6.5, 1, 7.5)], // ┐
  0x2514: [R(3, 6.5, 4, 1), R(3, 0, 1, 7.5)], // └
  0x2518: [R(0, 6.5, 4, 1), R(3, 0, 1, 7.5)], // ┘
  0x252c: [R(0, 6.5, 7, 1), R(3, 6.5, 1, 7.5)], // ┬
  0x2534: [R(0, 6.5, 7, 1), R(3, 0, 1, 7.5)], // ┴
  0x253c: [R(0, 6.5, 7, 1), R(3, 0, 1, 14)], // ┼
};
