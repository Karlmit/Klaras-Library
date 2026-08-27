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

type FieldKey = 'title' | 'authors' | 'series' | 'publisher' | 'pubdate' | 'description' | 'tags' | 'isbn'

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
      // Worth its own row: an ISBN is what the providers are searched by, so
      // filling one in is what makes the next lookup work at all.
      mk('isbn', 'ISBN',
         (book.identifiers ?? []).find((i) => i.scheme === 'isbn')?.value ?? '',
         result.identifiers?.isbn ?? ''),
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
  // The found value is a starting point, not a verdict. A provider will hand
  // back a title with the series glued on or a publisher in the wrong case,
  // and correcting it here beats taking it and fixing it afterwards.
  const [values, setValues] = useState<Record<string, string>>(
    () => Object.fromEntries(rows.map((r) => [r.key, r.found])),
  )
  const [cover, setCover] = useState(result.cover_url ?? '')
  const [takeCover, setTakeCover] = useState(!book.has_cover && !!result.cover_url)
  const [dims, setDims] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const toggle = (k: FieldKey) =>
    setPicked((p) => {
      const n = new Set(p)
      n.has(k) ? n.delete(k) : n.add(k)
      return n
    })

  const hasCoverRow = !!result.cover_url
  const total = rows.length + (hasCoverRow ? 1 : 0)
  const count = picked.size + (takeCover ? 1 : 0)
  const allOn = count === total && total > 0

  const toggleAll = () => {
    const on = !allOn
    setPicked(on ? new Set(rows.map((r) => r.key)) : new Set())
    if (hasCoverRow) setTakeCover(on)
  }

  const measure = (which: string) => (e: React.SyntheticEvent<HTMLImageElement>) => {
    const i = e.currentTarget
    setDims((d) => ({ ...d, [which]: `${i.naturalWidth}×${i.naturalHeight}px` }))
  }


  const list = (v: string) =>
    v.split(',').map((x) => x.trim()).filter(Boolean)

  const apply = async () => {
    setBusy(true)
    setError('')
    try {
      if (takeCover && cover.trim()) {
        await booksApi.fetchCover(bookId, cover.trim())
      }
      const patch: Partial<BookEdit> = {}
      for (const r of rows) {
        if (!picked.has(r.key)) continue
        const v = (values[r.key] ?? '').trim()
        if (r.key === 'authors') patch.authors = list(v)
        else if (r.key === 'tags') patch.tags = list(v)
        else if (r.key === 'pubdate') patch.pubdate = normaliseDate(v) ?? v
        else if (r.key === 'title') patch.title = v
        else if (r.key === 'series') patch.series = v
        else if (r.key === 'publisher') patch.publisher = v
        else if (r.key === 'description') patch.description = v
        else if (r.key === 'isbn') patch.isbn = v
      }
      onApply(patch)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="pick">
      <div className="pick__head">
        <button className="btn btn--sm btn--ghost" onClick={onBack}>← Other matches</button>
        <span className="pick__src">from {result.source}</span>
      </div>

      {error && <div className="error">{error}</div>}

      <label className="pick__all">
        <input type="checkbox" checked={allOn} onChange={toggleAll} />
        <span>Select all</span>
        <span className="hint">
          Ticked already where the book has nothing; anything that would replace what is
          there starts unticked.
        </span>
      </label>

      {hasCoverRow && (
        <div className={`pick__row pick__row--cover ${takeCover ? 'pick__row--on' : ''}`}>
          <input
            type="checkbox"
            checked={takeCover}
            aria-label="Replace the cover"
            onChange={() => setTakeCover((v) => !v)}
          />
          <div className="pick__body">
            <span className="pick__label">Cover</span>
            <input
              className="pick__input"
              value={cover}
              onChange={(e) => setCover(e.target.value)}
              spellCheck={false}
            />
          </div>
          <div className="pick__covers">
            <figure>
              <figcaption>New</figcaption>
              {cover.trim()
                ? <img src={remoteImage(cover.trim())} alt="" onLoad={measure('new')} />
                : <div className="pick__nocover">none</div>}
              <span>{dims.new}</span>
            </figure>
            <figure>
              <figcaption>Current</figcaption>
              {book.has_cover
                ? <img src={coverUrl(bookId, 'grid', book.updated_at)} alt="" />
                : <div className="pick__nocover">none</div>}
              {/* The file's own size, not the thumbnail's: this number is here
                  to be compared against the candidate's. */}
              <span>{book.cover_w ? `${book.cover_w}×${book.cover_h}px` : ''}</span>
            </figure>
          </div>
        </div>
      )}

      {rows.map((r) => (
        <div key={r.key} className={`pick__row ${picked.has(r.key) ? 'pick__row--on' : ''}`}>
          <input
            type="checkbox"
            checked={picked.has(r.key)}
            aria-label={r.label}
            onChange={() => toggle(r.key)}
          />
          <div className="pick__body">
            <span className="pick__label">{r.label}</span>
            {r.key === 'description' ? (
              <textarea
                className="pick__input pick__input--long"
                rows={5}
                value={values[r.key] ?? ''}
                onChange={(e) => setValues((v) => ({ ...v, [r.key]: e.target.value }))}
              />
            ) : (
              <input
                className="pick__input"
                value={values[r.key] ?? ''}
                onChange={(e) => setValues((v) => ({ ...v, [r.key]: e.target.value }))}
              />
            )}
            <span className="pick__was">
              {r.current ? `Currently: ${r.current}` : 'Currently: nothing'}
            </span>
          </div>
        </div>
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

