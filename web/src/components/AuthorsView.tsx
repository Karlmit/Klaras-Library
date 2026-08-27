import { useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useVirtualizer } from '@tanstack/react-virtual'
import { browseApi, portraitUrl, type AuthorEntry } from '../api'

/**
 * Every author in the library, as a wall of faces.
 *
 * Ten thousand of them, which is the whole design problem. Audiobookshelf's
 * version lists 59 and can afford a plain grid; here the grid is virtualised,
 * the list arrives once and is filtered in the browser, and the portraits are
 * fetched only for the row being looked at.
 *
 * Most cards will never have a photograph -- Wikidata knows the well-known
 * Swedish authors and almost none of the long tail -- so the placeholder is
 * the common case, not the exception. Rather than repeat one grey silhouette
 * ten thousand times, each is the author's initials on a tint derived from
 * their name: stable, distinguishable at a glance, and deliberately within the
 * library's own palette so a screen full of them reads as a design rather than
 * as a screen full of missing images.
 */
export function AuthorsView({ onPick }: { onPick: (id: number) => void }) {
  const { data, isLoading } = useQuery({
    queryKey: ['authors'],
    queryFn: browseApi.authors,
    staleTime: 5 * 60_000,
  })
  const { data: sweep } = useQuery({
    queryKey: ['portrait-status'],
    queryFn: browseApi.portraitStatus,
    // While the sweep is running the numbers move, so this refreshes rather
    // than sitting on a figure from when the page opened.
    refetchInterval: 30_000,
  })
  const [q, setQ] = useState('')
  const [minBooks, setMinBooks] = useState(1)
  // Most books first by default. Sorted by name, the first screen of a library
  // like this is [Bymbp] and "© Kazuo Ishiguro 1955" -- one-book oddities from
  // the import, which are also the last names the portrait sweep reaches. The
  // authors worth looking at, and the ones with pictures, were all buried.
  const [order, setOrder] = useState<'books' | 'name'>('books')
  const scroller = useRef<HTMLDivElement | null>(null)

  const authors = useMemo(() => {
    const all = data?.authors ?? []
    const needle = q.trim().toLowerCase()
    const list = all.filter(
      (a) => a.books >= minBooks && (!needle || a.name.toLowerCase().includes(needle)),
    )
    // The server already returns them in Swedish name order, so only the
    // by-books ordering needs doing here.
    return order === 'books'
      ? [...list].sort((x, y) => y.books - x.books || x.sort.localeCompare(y.sort, 'sv'))
      : list
  }, [data, q, minBooks, order])

  // Card width drives how many fit per row; the virtualiser works in rows.
  const [perRow, setPerRow] = useState(6)
  const measure = (el: HTMLDivElement | null) => {
    if (!el) return
    scroller.current = el
    const w = el.clientWidth
    const card = 150
    setPerRow(Math.max(2, Math.floor(w / card)))
  }

  const rows = Math.ceil(authors.length / perRow)
  const virt = useVirtualizer({
    count: rows,
    getScrollElement: () => scroller.current,
    estimateSize: () => 196,
    overscan: 3,
  })

  return (
    <div className="browse">
      <div className="browse__bar">
        <h1>
          {isLoading
            ? 'Authors'
            : authors.length === 1
              ? '1 author'
              : `${authors.length.toLocaleString('sv-SE')} authors`}
        </h1>
        <input
          className="browse__find"
          placeholder="Find an author…"
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />
        <label className="browse__min">
          <span>Sort</span>
          <select value={order} onChange={(e) => setOrder(e.target.value as 'books' | 'name')}>
            <option value="books">Most books</option>
            <option value="name">Name A–Ö</option>
          </select>
        </label>
        <label className="browse__min">
          <span>At least</span>
          <select value={minBooks} onChange={(e) => setMinBooks(Number(e.target.value))}>
            <option value={1}>1 book</option>
            <option value={2}>2 books</option>
            <option value={5}>5 books</option>
            <option value={10}>10 books</option>
          </select>
        </label>
      </div>

      {/* Answers "is this working?", which a wall of initials cannot. */}
      {sweep && sweep.checked < sweep.authors && (
        <p className="browse__sweep">
          Looking up pictures on Wikidata: {sweep.checked.toLocaleString('sv-SE')} of{' '}
          {sweep.authors.toLocaleString('sv-SE')} names checked,{' '}
          {sweep.found.toLocaleString('sv-SE')} found. Most Swedish authors are not on
          Wikidata at all — you can add a picture yourself on an author's page.
        </p>
      )}

      <div className="browse__scroll" ref={measure}>
        <div style={{ height: virt.getTotalSize(), position: 'relative' }}>
          {virt.getVirtualItems().map((row) => (
            <div
              key={row.key}
              className="browse__row"
              style={{
                position: 'absolute',
                top: 0,
                left: 0,
                width: '100%',
                height: row.size,
                transform: `translateY(${row.start}px)`,
                gridTemplateColumns: `repeat(${perRow}, minmax(0, 1fr))`,
              }}
            >
              {authors
                .slice(row.index * perRow, row.index * perRow + perRow)
                .map((a) => (
                  <AuthorCard key={a.id} author={a} onPick={onPick} />
                ))}
            </div>
          ))}
        </div>
        {!isLoading && !authors.length && (
          <p className="browse__empty">No author matches that.</p>
        )}
      </div>
    </div>
  )
}

function AuthorCard({
  author,
  onPick,
}: {
  author: AuthorEntry
  onPick: (id: number) => void
}) {
  // The list says whether a portrait exists, so a card that has none never
  // asks for one. Guessing and handling the 404 would mean a request and a
  // console error for the great majority of nine thousand authors.
  const [failed, setFailed] = useState(false)
  const showPhoto = author.has_portrait && !failed
  return (
    <button className="acard" onClick={() => onPick(author.id)} title={author.name}>
      <span className="acard__face" style={{ background: tintFor(author.name) }}>
        {showPhoto ? (
          <img
            src={portraitUrl(author.id)}
            alt=""
            loading="lazy"
            onError={() => setFailed(true)}
          />
        ) : (
          <span className="acard__initials">{initials(author.name)}</span>
        )}
      </span>
      <span className="acard__name">{author.name}</span>
      <span className="acard__count">
        {author.books === 1 ? '1 book' : `${author.books} books`}
      </span>
    </button>
  )
}

/** Up to two letters, skipping the punctuation a scraped name collects. */
function initials(name: string) {
  const parts = name
    .replace(/[^\p{L}\p{N}\s-]/gu, ' ')
    .split(/[\s-]+/)
    .filter(Boolean)
  if (!parts.length) return '?'
  const first = parts[0]?.[0] ?? ''
  const last = parts.length > 1 ? (parts[parts.length - 1]?.[0] ?? '') : ''
  return (first + last).toUpperCase() || '?'
}

/**
 * A stable colour for a name, kept inside the library's own hue range.
 *
 * Hashing a name to any hue at all would give a confetti wall; holding the hue
 * near the logo's violet and varying only how far around and how light keeps
 * ten thousand of these looking like one surface.
 */
function tintFor(name: string) {
  let h = 0
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0
  const hue = 250 + (h % 60) // 250–310: violet through to soft magenta
  const light = 78 + ((h >> 8) % 10) // a narrow band, so text stays readable
  return `hsl(${hue} 38% ${light}%)`
}
