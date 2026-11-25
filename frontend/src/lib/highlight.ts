/**
 * A light, dependency-free highlighter for code in chat replies: comments,
 * strings, numbers and keywords for common languages, and +/- lines for diffs.
 * It never changes the text, only splits it into coloured spans.
 */
export type TokenKind = 'plain' | 'comment' | 'string' | 'number' | 'keyword' | 'added' | 'removed' | 'meta'
export type Token = { kind: TokenKind; text: string }

const KEYWORDS: Record<string, string> = {
  js: 'async await break case catch class const continue default delete do else export extends false finally for from function if import in instanceof let new null of return static super switch this throw true try typeof undefined var void while yield',
  ts: 'abstract as async await break case catch class const continue declare default delete do else enum export extends false finally for from function if implements import in instanceof interface keyof let namespace new null of private protected public readonly return satisfies static super switch this throw true try type typeof undefined unknown never var void while yield',
  py: 'and as assert async await break class continue def del elif else except False finally for from global if import in is lambda None nonlocal not or pass raise return self True try while with yield',
  go: 'break case chan const continue default defer else fallthrough false for func go goto if import interface map nil package range return select struct switch true type var',
  rust: 'as async await break const continue crate else enum extern false fn for if impl in let loop match mod move mut pub ref return self Self static struct super trait true type unsafe use where while',
  sh: 'case do done elif else esac export fi for function if in local return then until while',
  sql: 'add all alter and as asc by create delete desc distinct drop from group having in index insert inner into is join key left like limit not null on or order outer primary references right select set table union unique update values where',
  json: 'true false null',
  cs: 'abstract as async await base bool break case catch class const continue default do else enum event false finally for foreach if in interface internal is new null override private protected public readonly return sealed static string struct switch this throw true try using var void while',
  java: 'abstract boolean break case catch class const continue default do else enum extends false final finally for if implements import instanceof interface new null package private protected public return static super switch this throw true try void while',
  css: 'important',
}

const ALIASES: Record<string, string> = {
  javascript: 'js', jsx: 'js', mjs: 'js', cjs: 'js', typescript: 'ts', tsx: 'ts',
  python: 'py', golang: 'go', rs: 'rust', bash: 'sh', shell: 'sh', zsh: 'sh', console: 'sh', ps1: 'sh', powershell: 'sh',
  postgres: 'sql', postgresql: 'sql', csharp: 'cs', 'c#': 'cs', kotlin: 'java', scss: 'css', jsonc: 'json', yml: 'yaml',
  patch: 'diff',
}

export function normalizeLang(lang: string): string {
  const l = lang.trim().toLowerCase()
  return ALIASES[l] ?? l
}

/** A friendly name for the code block's label. */
export function langLabel(lang: string): string {
  const l = lang.trim()
  if (!l) return 'Code'
  const names: Record<string, string> = {
    js: 'JavaScript', ts: 'TypeScript', py: 'Python', go: 'Go', rust: 'Rust', sh: 'Shell', sql: 'SQL', json: 'JSON',
    cs: 'C#', java: 'Java', css: 'CSS', html: 'HTML', yaml: 'YAML', diff: 'Diff', md: 'Markdown', markdown: 'Markdown',
  }
  return names[normalizeLang(l)] ?? l
}

const HASH_COMMENT = new Set(['py', 'sh', 'yaml', 'toml', 'rb', 'r'])
const DASH_COMMENT = new Set(['sql', 'lua', 'hs'])

function lineComment(lang: string): string[] {
  if (HASH_COMMENT.has(lang)) return ['#']
  if (DASH_COMMENT.has(lang)) return ['--']
  if (lang === 'json' || lang === 'css' || lang === 'html' || lang === '') return []
  return ['//']
}

export function highlight(code: string, lang: string): Token[] {
  const l = normalizeLang(lang)
  if (l === 'diff') return diffTokens(code)
  const words = new Set((KEYWORDS[l] ?? '').split(' ').filter(Boolean))
  // Unknown languages get strings, numbers and comments only (safe for anything).
  const comments = lineComment(l)
  const blockComment = l !== 'py' && l !== 'sh' && l !== 'yaml' && l !== 'json' && l !== ''
  const out: Token[] = []
  const push = (kind: TokenKind, text: string) => {
    if (!text) return
    const last = out[out.length - 1]
    if (last && last.kind === kind) last.text += text
    else out.push({ kind, text })
  }
  let i = 0
  const n = code.length
  while (i < n) {
    const c = code[i]
    const rest = code.startsWith.bind(code)
    if (blockComment && rest('/*', i)) {
      const end = code.indexOf('*/', i + 2)
      const stop = end < 0 ? n : end + 2
      push('comment', code.slice(i, stop))
      i = stop
      continue
    }
    const lc = comments.find((m) => rest(m, i))
    if (lc && (lc !== '#' || l !== 'css')) {
      const end = code.indexOf('\n', i)
      const stop = end < 0 ? n : end
      push('comment', code.slice(i, stop))
      i = stop
      continue
    }
    if (c === '"' || c === "'" || c === '`') {
      let j = i + 1
      while (j < n && code[j] !== c) {
        if (code[j] === '\\') j++
        else if (code[j] === '\n' && c !== '`') break
        j++
      }
      const stop = Math.min(n, j + 1)
      push('string', code.slice(i, stop))
      i = stop
      continue
    }
    if (/[0-9]/.test(c) && !/[\w$]/.test(code[i - 1] ?? '')) {
      const m = /^(0x[0-9a-f_]+|[0-9][0-9_]*(\.[0-9_]+)?(e[+-]?[0-9]+)?)/i.exec(code.slice(i, i + 40))
      if (m) {
        push('number', m[0])
        i += m[0].length
        continue
      }
    }
    if (/[A-Za-z_$]/.test(c)) {
      let j = i + 1
      while (j < n && /[\w$]/.test(code[j])) j++
      const word = code.slice(i, j)
      push(words.has(l === 'sql' ? word.toLowerCase() : word) ? 'keyword' : 'plain', word)
      i = j
      continue
    }
    push('plain', c)
    i++
  }
  return out
}

function diffTokens(code: string): Token[] {
  return code.split(/(?<=\n)/).map((line) => ({
    kind: line.startsWith('+++') || line.startsWith('---') || line.startsWith('@@')
      ? 'meta'
      : line.startsWith('+')
        ? 'added'
        : line.startsWith('-')
          ? 'removed'
          : 'plain',
    text: line,
  }))
}
