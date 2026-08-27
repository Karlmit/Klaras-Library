import { useMemo, useState } from 'react'
import { booksApi, coverUrl, remoteImage, type Book, type BookEdit, type MetadataResult } from '../api'

/** Providers return years, year-months and full dates; the input needs a date. */
export function normaliseDate(s?: string): string | undefined {
  if (!s) return undefined
  if (/^\d{4}$/.test(s)) return `${s}-01-01`
  if (/^\d{4}-\d{2}$/.test(s)) return `${s}-01`
  if (/^\d{4}-\d{2}-\d{2}/.test(s)) return s.slice(0, 10)
  return undefined
}

type FieldKey = 'title' | 'authors' | 'series' | 'publisher' | 'pubdate' | 'description' | 'tags'

interface Row {
  key: FieldKey
  label: string
  current: string
  found: string
  /** Ticked to begin with. */
  fill: boolean
}

/**
 * Choose what to take from a search result, field by field.
 *
 * Applying a whole result was the old behaviour and it was too blunt: providers
 * routinely return a different edition, so a lookup that fixes a missing blurb
 * would also overwrite a title someone had corrected by hand. Each field is now
 * its own decision.
 *
 * What starts ticked encodes the safe default: fields we have nothing for are
 * on, fields where we already hold something are off. Filling a gap is almost
 * always wanted; replacing existing data is a choice someone should make
 * deliberately.
 */
// Values differing only by case, spacing or surrounding punctuation are the
// same value as far as someone reviewing the list is concerned.
function sameValue(a: string, b: string) {
  const norm = (v: string) =>
    v.toLowerCase().replace(/[\s.,;:'"\u2018\u2019\u201c\u201d]+/g, ' ').trim()
  return norm(a) === norm(b)
}

export function LookupPicker({
  bookId, book, result, onApply, onBack,
}: {
  bookId: number
  book: Book
  result: MetadataResult
  onApply: (patch: Partial<BookEdit>) => void
  onBack: () => void
}) {
  const { rows, matched } = useMemo(() => {
    const mk = (key: FieldKey, label: string, current: string, found: string): Row | null =>
      found.trim() ? { key, label, current, found, fill: !current.trim() } : null
    const all = [
      mk('title', 'Title', book.title ?? '', result.title ?? ''),
      mk('authors', 'Authors', (book.authors ?? []).join(', '), (result.authors ?? []).join(', ')),
      mk('series', 'Series', book.series ?? '', result.series ?? ''),
      mk('publisher', 'Publisher', book.publisher ?? '', result.publisher ?? ''),
      mk('pubdate', 'Published', (book.pubdate ?? '').slice(0, 10),
         normaliseDate(result.pubdate) ?? ''),
      mk('description', 'Description', book.description ?? '', result.description ?? ''),
      mk('tags', 'Categories', (book.tags ?? []).join(', '), (result.tags ?? []).join(', ')),
    ].filter(Boolean) as Row[]
    // A row whose two sides already say the same thing is not a choice; it is
    // noise in a list someone has to read. Count them instead, so the panel can
    // say the lookup agreed rather than silently dropping the field.
    const rows = all.filter((r) => !sameValue(r.current, r.found))
    return { rows, matched: all.length - rows.length }
  }, [book, result])

  const [picked, setPicked] = useState<Set<FieldKey>>(
    () => new Set(rows.filter((r) => r.fill).map((r) => r.key)),
  )
  const [takeCover, setTakeCover] = useState(!book.has_cover && !!result.cover_url)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const toggle = (k: FieldKey) =>
    setPicked((p) => {
      const n = new Set(p)
      n.has(k) ? n.delete(k) : n.add(k)
      return n
    })

  const apply = async () => {
    setBusy(true)
    setError('')
    try {
      if (takeCover && result.cover_url) {
        await booksApi.fetchCover(bookId, result.cover_url)
      }
      const patch: Partial<BookEdit> = {}
      for (const r of rows) {
        if (!picked.has(r.key)) continue
        if (r.key === 'authors') patch.authors = result.authors
        else if (r.key === 'tags') patch.tags = result.tags
        else if (r.key === 'pubdate') patch.pubdate = normaliseDate(result.pubdate)
        else if (r.key === 'title') patch.title = result.title
        else if (r.key === 'series') patch.series = result.series
        else if (r.key === 'publisher') patch.publisher = result.publisher
        else if (r.key === 'description') patch.description = result.description
      }
      onApply(patch)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const count = picked.size + (takeCover ? 1 : 0)

  return (
    <div className="pick">
      <div className="pick__head">
        <button className="btn btn--sm btn--ghost" onClick={onBack}>← Other matches</button>
        <span className="pick__src">from {result.source}</span>
      </div>

      {error && <div className="error">{error}</div>}

      <p className="hint" style={{ marginTop: 0 }}>
        Ticked by default where the book has nothing. Anything that would replace what is
        already there starts unticked.
      </p>

      {result.cover_url && (
        <label className={`pick__row pick__row--cover ${takeCover ? 'pick__row--on' : ''}`}>
          <input type="checkbox" checked={takeCover} onChange={() => setTakeCover((v) => !v)} />
          <span className="pick__label">Cover</span>
          <div className="pick__covers">
            <figure>
              {book.has_cover
                ? <img src={coverUrl(bookId, 'grid')} alt="" />
                : <div className="pick__nocover">none</div>}
              <figcaption>now</figcaption>
            </figure>
            <span className="pick__arrow" aria-hidden="true">→</span>
            <figure>
              <img src={remoteImage(result.cover_url)} alt="" />
              <figcaption>found</figcaption>
            </figure>
          </div>
        </label>
      )}

      {rows.map((r) => (
        <label key={r.key} className={`pick__row ${picked.has(r.key) ? 'pick__row--on' : ''}`}>
          <input type="checkbox" checked={picked.has(r.key)} onChange={() => toggle(r.key)} />
          <span className="pick__label">{r.label}</span>
          <div className="pick__vals">
            <span className="pick__now">{r.current || <em>empty</em>}</span>
            <span className="pick__arrow" aria-hidden="true">→</span>
            <span className="pick__new">{r.found}</span>
          </div>
        </label>
      ))}

      <div className="pick__foot">
        <button className="btn" disabled={count === 0 || busy} onClick={() => void apply()}>
          {busy ? 'Applying…' : count === 0 ? 'Nothing selected' : `Use ${count} of these`}
        </button>
        <span className="hint">
          {matched > 0 && (
            <>
              {matched === 1
                ? '1 field already matches and is not listed. '
                : `${matched} fields already match and are not listed. `}
            </>
          )}
          Fields go into the form below — nothing is saved until you press Save.
          {takeCover && ' The cover is replaced immediately.'}
        </span>
      </div>
    </div>
  )
}
