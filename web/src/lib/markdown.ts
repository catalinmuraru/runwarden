// A deliberately small Markdown renderer for script descriptions. Everything is HTML-escaped
// first; only the constructs below are turned back into markup, so the output is safe for
// v-html. Supported: [links](https://...), **bold** / __bold__, *italic* / _italic_, `code`,
// "> " quote lines and line breaks.

function escapeHTML(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}

function inline(text: string): string {
  const code: string[] = []
  let s = escapeHTML(text).replace(/`([^`]+)`/g, (_, c: string) => {
    code.push(`<code>${c}</code>`)
    return `\u0000${code.length - 1}\u0000`
  })
  s = s.replace(
    /\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g,
    '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>',
  )
  s = s.replace(/(\*\*|__)(?=\S)(.+?)(?<=\S)\1/g, '<strong>$2</strong>')
  s = s.replace(/(^|[^\w*])\*(?=\S)([^*]+?)(?<=\S)\*(?!\w)/g, '$1<em>$2</em>')
  s = s.replace(/(^|[^\w])_(?=\S)([^_]+?)(?<=\S)_(?!\w)/g, '$1<em>$2</em>')
  return s.replace(/\u0000(\d+)\u0000/g, (_, i: string) => code[Number(i)])
}

export function renderMarkdown(src: string): string {
  const out: string[] = []
  let quote: string[] = []
  const flushQuote = () => {
    if (quote.length) out.push(`<blockquote>${quote.join('<br>')}</blockquote>`)
    quote = []
  }
  for (const line of src.replace(/\r\n/g, '\n').replace(/\n+$/, '').split('\n')) {
    const m = /^\s*>\s?(.*)$/.exec(line)
    if (m) {
      quote.push(inline(m[1]))
      continue
    }
    flushQuote()
    out.push(inline(line))
  }
  flushQuote()
  return out.join('<br>').replace(/<br>(<blockquote>)/g, '$1').replace(/(<\/blockquote>)<br>/g, '$1')
}

/** First line of a description as plain text, for lists. */
export function summary(src: string | undefined): string {
  const first = (src ?? '').split(/\r?\n/).find((l) => l.trim() !== '') ?? ''
  return first.replace(/[*_`>]/g, '').replace(/\[([^\]]+)\]\([^)]+\)/g, '$1').trim()
}
