// Uang di API = string integer minor unit (sen), tidak pernah float (ADR-L04). UI menerima input
// rupiah utuh ("50000" atau "50.000") dan mengubahnya ke minor unit dengan aritmetika string.

/** "50.000" / "50000" → "5000000" (minor unit). null kalau bukan bilangan rupiah utuh positif. */
export function rupiahToMinor(input: string): string | null {
  const digits = input.trim().replace(/\./g, '')
  if (!/^[1-9][0-9]{0,11}$/.test(digits)) return null
  return digits + '00'
}

/** "5000000" → "Rp50.000" (sen ditampilkan hanya kalau bukan nol). */
export function formatMinor(minor: string): string {
  const neg = minor.startsWith('-')
  const s = (neg ? minor.slice(1) : minor).padStart(3, '0')
  const rupiah = s.slice(0, -2).replace(/^0+(?=\d)/, '')
  const sen = s.slice(-2)
  const grouped = rupiah.replace(/\B(?=(\d{3})+(?!\d))/g, '.')
  return (neg ? '-' : '') + 'Rp' + grouped + (sen === '00' ? '' : ',' + sen)
}
