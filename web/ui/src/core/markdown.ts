// A small markdown renderer.
//
// Written out rather than pulled from a CDN so the viewer works offline and so
// the escaping is auditable in one place. Input is escaped FIRST and only this
// function's own tags are added afterwards, which is what makes it safe to feed
// the result to v-html: by then there is no caller-supplied markup left in it.

function esc(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
}

function inline(s: string): string {
  return s
    .replace(/`([^`]+)`/g, (_, c: string) => `<code>${c}</code>`)
    .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
    .replace(/(^|\W)\*([^*]+)\*/g, '$1<em>$2</em>')
    .replace(
      /\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)/g,
      '<a href="$2" rel="noopener noreferrer" target="_blank">$1</a>',
    )
}

export function markdown(src: string): string {
  const lines = esc(src).split(/\r?\n/)
  const out: string[] = []
  let inCode = false
  let listType: 'ul' | 'ol' | null = null
  let para: string[] = [] // buffered paragraph lines
  let item: string[] | null = null // buffered list-item lines

  // Markdown joins consecutive non-blank lines into one block. Emitting each
  // source line as its own <p> is what makes a hard-wrapped document render
  // with a blank line between every line, and what restarts an <ol> at 1 on
  // every wrapped item.
  const flushPara = () => {
    if (para.length) {
      out.push(`<p>${inline(para.join(' '))}</p>`)
      para = []
    }
  }
  const flushItem = () => {
    if (item !== null) {
      out.push(`<li>${inline(item.join(' '))}</li>`)
      item = null
    }
  }
  const closeList = () => {
    flushItem()
    if (listType) {
      out.push(`</${listType}>`)
      listType = null
    }
  }
  const closeAll = () => {
    flushPara()
    closeList()
  }

  const isTableRow = (s: string) => /^\s*\|.*\|\s*$/.test(s)
  const cells = (s: string) =>
    s
      .replace(/^\s*\|/, '')
      .replace(/\|\s*$/, '')
      .split('|')
      .map((c) => c.trim())

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]

    if (/^\s*```/.test(line)) {
      closeAll()
      out.push(inCode ? '</code></pre>' : '<pre><code>')
      inCode = !inCode
      continue
    }
    if (inCode) {
      out.push(line)
      continue
    }

    // A table is a row, a |---|---| separator, then body rows.
    if (isTableRow(line) && i + 1 < lines.length && /^\s*\|[\s|:-]+\|\s*$/.test(lines[i + 1])) {
      closeAll()
      out.push(
        `<table><thead><tr>${cells(line)
          .map((c) => `<th>${inline(c)}</th>`)
          .join('')}</tr></thead><tbody>`,
      )
      i += 2
      for (; i < lines.length && isTableRow(lines[i]); i++) {
        out.push(
          `<tr>${cells(lines[i])
            .map((c) => `<td>${inline(c)}</td>`)
            .join('')}</tr>`,
        )
      }
      i--
      out.push('</tbody></table>')
      continue
    }

    const h = /^(#{1,6})\s+(.*)$/.exec(line)
    if (h) {
      closeAll()
      const lvl = Math.min(h[1].length, 3)
      out.push(`<h${lvl}>${inline(h[2])}</h${lvl}>`)
      continue
    }
    if (/^\s*([-*_])\s*\1\s*\1[\s\-*_]*$/.test(line)) {
      closeAll()
      out.push('<hr>')
      continue
    }
    // Blockquote markers are matched as &gt; because esc() has already run.
    // Testing for a literal '>' here silently never matches, which is how a
    // quote ends up rendered as a paragraph with its markers showing.
    if (/^&gt;\s?/.test(line)) {
      closeAll()
      const quote = [line.replace(/^&gt;\s?/, '')]
      while (i + 1 < lines.length && /^&gt;\s?/.test(lines[i + 1])) {
        quote.push(lines[++i].replace(/^&gt;\s?/, ''))
      }
      out.push(`<blockquote>${inline(quote.join(' '))}</blockquote>`)
      continue
    }

    const ul = /^\s*[-*+]\s+(.*)$/.exec(line)
    const ol = /^\s*\d+\.\s+(.*)$/.exec(line)
    const bullet = ul ?? ol
    if (bullet) {
      flushPara()
      const want = ul ? 'ul' : 'ol'
      flushItem()
      if (listType !== want) {
        closeList()
        out.push(`<${want}>`)
        listType = want
      }
      item = [bullet[1]]
      continue
    }

    if (!line.trim()) {
      closeAll()
      continue
    }

    // A non-blank line inside a list continues the current item; otherwise it
    // continues the current paragraph.
    if (item !== null) {
      item.push(line.trim())
      continue
    }
    para.push(line.trim())
  }

  closeAll()
  if (inCode) out.push('</code></pre>')
  return out.join('\n')
}
