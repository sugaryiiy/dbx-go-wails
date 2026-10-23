const KEY_BASE = 65536;
const WORDS_PER_BLOCK = 128;
const CELLS_PER_BLOCK = WORDS_PER_BLOCK * 32;

interface MatchBlock {
  index: number;
  words: Uint32Array;
  before: number;
  count: number;
}

function bitCount(value: number): number {
  value -= (value >>> 1) & 0x55555555;
  value = (value & 0x33333333) + ((value >>> 2) & 0x33333333);
  return (((value + (value >>> 4)) & 0x0f0f0f0f) * 0x01010101) >>> 24;
}

/**
 * Ordered cell matches share one bitmap for navigation and paint membership.
 * Allocate a 512-byte block only when it contains a match. Dense searches use
 * one bit per cell instead of both a number array and a JavaScript Set.
 */
export class DataGridSearchMatchIndex implements ReadonlySet<number> {
  private readonly blocks: MatchBlock[] = [];
  private readonly blocksByIndex = new Map<number, MatchBlock>();
  private count = 0;

  constructor(private readonly columnCount: number) {}

  get size(): number {
    return this.count;
  }

  get bitmapBytes(): number {
    return this.blocks.length * WORDS_PER_BLOCK * Uint32Array.BYTES_PER_ELEMENT;
  }

  /** The scanner appends cells in header/row/column order. */
  appendCell(cellIndex: number): void {
    const blockIndex = Math.floor(cellIndex / CELLS_PER_BLOCK);
    let block = this.blocksByIndex.get(blockIndex);
    if (!block) {
      block = { index: blockIndex, words: new Uint32Array(WORDS_PER_BLOCK), before: this.count, count: 0 };
      this.blocksByIndex.set(blockIndex, block);
      this.blocks.push(block);
    }
    const offset = cellIndex % CELLS_PER_BLOCK;
    const word = Math.floor(offset / 32);
    const bit = 1 << (offset % 32);
    if ((block.words[word]! & bit) !== 0) return;
    block.words[word] = block.words[word]! | bit;
    block.count++;
    this.count++;
  }

  private keyForCell(cellIndex: number): number {
    return Math.floor(cellIndex / this.columnCount) * KEY_BASE + (cellIndex % this.columnCount);
  }

  keyAt(index: number): number | undefined {
    if (!Number.isInteger(index) || index < 0 || index >= this.count) return undefined;
    let low = 0;
    let high = this.blocks.length - 1;
    while (low < high) {
      const middle = (low + high) >>> 1;
      const block = this.blocks[middle]!;
      if (index >= block.before + block.count) low = middle + 1;
      else high = middle;
    }
    const block = this.blocks[low]!;
    let remaining = index - block.before;
    for (let wordIndex = 0; wordIndex < block.words.length; wordIndex++) {
      let word = block.words[wordIndex]!;
      const count = bitCount(word);
      if (remaining >= count) {
        remaining -= count;
        continue;
      }
      while (remaining-- > 0) word &= word - 1;
      const bit = 31 - Math.clz32(word & -word);
      return this.keyForCell(block.index * CELLS_PER_BLOCK + wordIndex * 32 + bit);
    }
    return undefined;
  }

  has(key: number): boolean {
    if (!Number.isSafeInteger(key) || key < 0 || this.columnCount === 0) return false;
    const column = key % KEY_BASE;
    if (column >= this.columnCount) return false;
    const cellIndex = Math.floor(key / KEY_BASE) * this.columnCount + column;
    const block = this.blocksByIndex.get(Math.floor(cellIndex / CELLS_PER_BLOCK));
    if (!block) return false;
    const offset = cellIndex % CELLS_PER_BLOCK;
    return (block.words[Math.floor(offset / 32)]! & (1 << (offset % 32))) !== 0;
  }

  *values(): SetIterator<number> {
    for (const block of this.blocks) {
      for (let wordIndex = 0; wordIndex < block.words.length; wordIndex++) {
        let word = block.words[wordIndex]!;
        while (word !== 0) {
          const bit = 31 - Math.clz32(word & -word);
          yield this.keyForCell(block.index * CELLS_PER_BLOCK + wordIndex * 32 + bit);
          word &= word - 1;
        }
      }
    }
  }

  keys(): SetIterator<number> {
    return this.values();
  }

  *entries(): SetIterator<[number, number]> {
    for (const key of this.values()) yield [key, key];
  }

  forEach(callback: (value: number, value2: number, set: ReadonlySet<number>) => void, thisArg?: unknown): void {
    for (const key of this.values()) callback.call(thisArg, key, key, this);
  }

  [Symbol.iterator](): SetIterator<number> {
    return this.values();
  }
}
