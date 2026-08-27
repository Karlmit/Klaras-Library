import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { booksApi, remoteImage, type CoverCandidate } from '../api'

/**
 * Offers the covers the providers know about.
 *
 * Two steps on purpose. Clicking a cover only selects it; replacing the book's
 * own cover takes a second, explicit press. A cover is the one piece of
 * metadata someone recognises a book by, and a provider's top hit is regularly
 * the wrong edition -- so nothing here changes a book until it is asked for.
 */
export function CoverFinder({
  bookId,
  onUse,
  onClose,
  busy,
}: {
  bookId: number
  onUse: (url: string) => void
  onClose: () => void
  busy: boolean
}) {
  const { data, isLoading, error } = useQuery({
    queryKey: ['covers', bookId],
    queryFn: () => booksApi.coverCandidates(bookId),
    staleTime: 5 * 60_000,
  })
  const [picked, setPicked] = useState<CoverCandidate | null>(null)
  // A candidate address is not a promise that an image is there: Open Library
  // answers 404 for an ISBN it has no picture for. Dropping the ones that fail
  // to load is the only way to know.
  const [broken, setBroken] = useState<Set<string>>(new Set())
  const [size, setSize] = useState<Record<string, string>>({})

  const all = data?.candidates ?? []
  const shown = all.filter((c) => !broken.has(c.url))
  const failed = (data?.sources ?? []).filter((s) => s.error)

  return (
    <div className="covers">
      <div className="covers__head">
        <strong>Covers found online</strong>
        <button className="btn btn--ghost btn--sm" onClick={onClose}>
          Close
        </button>
      </div>

      {isLoading && <p style={{ color: 'var(--text-muted)' }}>Looking…</p>}
      {!!error && <div className="error">{(error as Error).message}</div>}
      {failed.map((s) => (
        <div key={s.name} className="warn" style={{ marginBottom: 8 }}>
          {s.name}: {s.error}
        </div>
      ))}
      {!isLoading && !shown.length && (
        <p style={{ color: 'var(--text-muted)' }}>
          No covers found for this book.
        </p>
      )}

      <div className="covers__grid">
        {shown.map((c) => (
          <button
            key={c.url}
            type="button"
            className={`covers__item ${picked?.url === c.url ? 'covers__item--on' : ''}`}
            onClick={() => setPicked(c)}
            title={c.title ? `${c.source} — ${c.title}` : c.source}
          >
            <img
              src={remoteImage(c.url)}
              alt=""
              loading="lazy"
              onError={() => setBroken((b) => new Set(b).add(c.url))}
              onLoad={(e) => {
                const i = e.currentTarget
                setSize((s) => ({ ...s, [c.url]: `${i.naturalWidth}×${i.naturalHeight}` }))
              }}
            />
            <span className="covers__meta">
              {c.source}
              {size[c.url] && <em>{size[c.url]}</em>}
            </span>
          </button>
        ))}
      </div>

      {picked && (
        <div className="covers__foot">
          <button className="btn" disabled={busy} onClick={() => onUse(picked.url)}>
            {busy ? 'Replacing…' : 'Use this cover'}
          </button>
          <span className="hint">
            Replaces the cover on disk. Nothing else about the book changes.
          </span>
        </div>
      )}
    </div>
  )
}
