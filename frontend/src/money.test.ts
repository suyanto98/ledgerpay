import { describe, expect, it } from 'vitest'
import { formatMinor, rupiahToMinor } from './money'

describe('money', () => {
  it('converts rupiah input to minor units without floats', () => {
    expect(rupiahToMinor('50000')).toBe('5000000')
    expect(rupiahToMinor('50.000')).toBe('5000000')
    expect(rupiahToMinor('0')).toBeNull()
    expect(rupiahToMinor('12,5')).toBeNull()
    expect(rupiahToMinor('-100')).toBeNull()
    expect(rupiahToMinor('1e5')).toBeNull()
  })
  it('formats minor units', () => {
    expect(formatMinor('0')).toBe('Rp0')
    expect(formatMinor('5000000')).toBe('Rp50.000')
    expect(formatMinor('1111100')).toBe('Rp11.111')
    expect(formatMinor('150')).toBe('Rp1,50')
    expect(formatMinor('100000000000')).toBe('Rp1.000.000.000')
  })
})
