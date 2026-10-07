import { describe, expect, it } from 'vitest'
import { decodeBytes } from './terminalProtocol'

describe('terminal protocol byte decoding', () => {
  it('accepts a legacy null empty payload without accepting other invalid types', () => {
    // Older hosts sent null, which atob coerced into bytes ending in a phantom "e".
    expect(decodeBytes(null)).toEqual(new Uint8Array())
    expect(decodeBytes(undefined)).toBeNull()
    expect(decodeBytes(42)).toBeNull()
  })
})
