import { describe, expect, it } from 'vitest'
import { renderMarkdown, summary } from './markdown'

describe('renderMarkdown', () => {
  it('escapes HTML', () => {
    expect(renderMarkdown('<script>alert(1)</script> & "x"')).toBe(
      '&lt;script&gt;alert(1)&lt;/script&gt; &amp; &quot;x&quot;',
    )
  })

  it('renders links only for http(s)', () => {
    expect(renderMarkdown('[guide](https://example.com/a?b=1&c=2)')).toBe(
      '<a href="https://example.com/a?b=1&amp;c=2" target="_blank" rel="noopener noreferrer">guide</a>',
    )
    expect(renderMarkdown('[x](javascript:alert(1))')).toBe('[x](javascript:alert(1))')
    expect(renderMarkdown('[x](https://e.com" onmouseover="alert(1))')).not.toContain('" onmouseover')
  })

  it('renders emphasis, code and nesting', () => {
    expect(renderMarkdown('**bold** and _it_ and `a_b*c`')).toBe(
      '<strong>bold</strong> and <em>it</em> and <code>a_b*c</code>',
    )
    expect(renderMarkdown('__[Docs](https://example.com)__')).toBe(
      '<strong><a href="https://example.com" target="_blank" rel="noopener noreferrer">Docs</a></strong>',
    )
    expect(renderMarkdown('snake_case_name stays')).toBe('snake_case_name stays')
  })

  it('renders quotes and line breaks', () => {
    expect(renderMarkdown('one\n> note\n> more\ntwo\n')).toBe(
      'one<blockquote>note<br>more</blockquote>two',
    )
  })
})

describe('summary', () => {
  it('returns the first line as plain text', () => {
    expect(summary('\n**Adds** [users](https://x)\nsecond')).toBe('Adds users')
    expect(summary(undefined)).toBe('')
  })
})
