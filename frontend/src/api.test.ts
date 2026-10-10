import { describe, expect, it } from 'vitest'
import { uuidV4 } from './api'

const V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/

describe('uuidV4 (fallback newIdempotencyKey di HTTP non-localhost)', () => {
  it('sets version and variant bits', () => {
    expect(uuidV4(new Uint8Array(16).fill(0xff))).toBe('ffffffff-ffff-4fff-bfff-ffffffffffff')
    expect(uuidV4(new Uint8Array(16))).toBe('00000000-0000-4000-8000-000000000000')
  })
  it('produces distinct valid v4 UUIDs from random bytes', () => {
    const ids = new Set(Array.from({ length: 1000 }, () => uuidV4(crypto.getRandomValues(new Uint8Array(16)))))
    expect(ids.size).toBe(1000)
    for (const id of ids) expect(id).toMatch(V4)
  })
})
