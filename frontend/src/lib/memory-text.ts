/**
 * Memory text is usually a plain sentence, but some memories (the project
 * brief above all) are markdown. Cards show a plain-text preview; the expanded
 * card renders the markdown.
 */

const MARKDOWN_HINT = /^(#{1,6}\s|\s*[-*+]\s|\s*\d+\.\s|```|>\s)|\*\*[^*]+\*\*|\[[^\]]+\]\([^)]+\)/m

export function looksLikeMarkdown(text: string): boolean {
  return MARKDOWN_HINT.test(text)
}

/** A leading "# Heading" line, which reads better as the card title. */
export function markdownTitle(text: string): string {
  const m = /^\s*#\s+(.+?)\s*#*\s*$/m.exec(text.split('\n', 1)[0] ?? '')
  return m ? m[1] : ''
}

/** Drops the leading "# Heading" line (shown as the title instead). */
export function withoutTitle(text: string): string {
  return markdownTitle(text) ? text.replace(/^[^\n]*\n?/, '').replace(/^\s+/, '') : text
}

/** Markdown to one line of plain text for previews. */
export function stripMarkdown(text: string): string {
  const lines = text
    .replace(/```[^\n]*\n([\s\S]*?)```/g, '$1')
    .split('\n')
    .map((line) => {
      let l = line.trim()
      if (!l) return ''
      const heading = /^#{1,6}\s+(.*?)\s*#*$/.exec(l)
      if (heading) l = heading[1].replace(/[:.]?$/, ':')
      l = l
        .replace(/^>\s?/, '')
        .replace(/^([-*+]|\d+\.)\s+(\[[ xX]\]\s+)?/, '')
        .replace(/!\[([^\]]*)\]\([^)]*\)/g, '$1')
        .replace(/\[([^\]]+)\]\([^)]*\)/g, '$1')
        .replace(/(\*\*|__)(.+?)\1/g, '$2')
        .replace(/(^|[^\w*])[*_]([^*_\n]+)[*_](?=[^\w*]|$)/g, '$1$2')
        .replace(/`([^`]+)`/g, '$1')
        .replace(/^\|?\s*:?-{3,}.*$/, '')
        .replace(/\s*\|\s*/g, ' · ')
      return l.trim()
    })
    .filter(Boolean)
  return lines.join(' ').replace(/\s+/g, ' ').trim()
}
