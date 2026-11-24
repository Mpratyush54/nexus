const COMMON = ['password', 'qwerty', '123456', '12345678', 'letmein', 'welcome', 'nexus', 'iloveyou', 'admin', 'abc123']

/** A small, honest strength estimate: length matters most, then variety. */
export function passwordScore(pw: string, others: string[] = []): { score: 0 | 1 | 2 | 3 | 4; label: string; tip: string } {
  if (pw.length < 8) return { score: 1, label: 'Too short', tip: `Use at least 8 characters (${8 - pw.length} more).` }
  const lower = pw.toLowerCase()
  const guessable =
    COMMON.some((c) => lower.includes(c)) ||
    others.some((o) => o && o.length >= 3 && lower.includes(o.toLowerCase())) ||
    /^(.)\1+$/.test(pw) ||
    /^(?:0123|1234|2345|3456|4567|5678|6789|abcd)/i.test(pw)
  if (guessable) return { score: 1, label: 'Easy to guess', tip: 'Avoid common words, your name or email.' }
  const kinds = [/[a-z]/, /[A-Z]/, /\d/, /[^A-Za-z0-9]/].filter((r) => r.test(pw)).length
  let score = 1
  if (pw.length >= 10) score++
  if (pw.length >= 14) score++
  if (kinds >= 3) score++
  if (pw.length >= 18 && kinds >= 2) score++
  const s = Math.min(4, Math.max(2, score)) as 2 | 3 | 4
  if (s === 2) return { score: 2, label: 'Okay', tip: 'Longer is stronger: try 12+ characters or a few random words.' }
  if (s === 3) return { score: 3, label: 'Strong', tip: 'Good password.' }
  return { score: 4, label: 'Very strong', tip: 'Great password.' }
}

export const looksLikeEmail = (value: string) => /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value.trim())
