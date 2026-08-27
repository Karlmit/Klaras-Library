import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, coverUrl, deleteApi } from '../api'
import { AddToShelf } from './AddToShelf'

interface Props {
  bookId: number
  onClose: () => void
  onFilter: (patch: { author?: string; tag?: string; series?: string }) => void
  onEdit: (id: number) => void
  onRead: (id: number, title: string, format: string) => void
  canEdit: boolean
}

const REVIEW_REASONS: Record<string, string> = {
  author_name_has_separator:
    'The author name contains ";" or "|", so it is probably two authors merged into one record, or a sort name stored as a display name.',
  implausible_pubdate: 'The publication date is before 1450 or more than two years in the future.',
  no_files: 'No file was found on disk for this book.',
  no_epub: 'There is no EPUB, so this book cannot be converted to KEPUB for a Kobo.',
  duplicate_title_author: 'Another book has exactly this title and author list.',
  empty_title: 'The title is missing or a placeholder.',
}

export function BookDetail({ bookId, onClose, onFilter, onEdit, onRead, canEdit }: Props) {
  const [shelfOpen, setShelfOpen] = useState(false)
  const qc = useQueryClient()
  const { data: book, isLoading } = useQuery({
    queryKey: ['book', bookId],
    queryFn: () => api.book(bookId),
  })

  const del = useMutation({
    mutationFn: () => deleteApi.one(bookId, false),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['books'] })
      void qc.invalidateQueries({ queryKey: ['facets'] })
      onClose()
    },
  })

  // Escape closes the drawer, which is what every drawer everywhere does.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  return (
    <>
      <div className="drawer-backdrop" onClick={onClose} />
      <aside className="drawer" role="dialog" aria-label="Book details" aria-modal="true">
        <button className="drawer__close" onClick={onClose} aria-label="Close">
          ×
        </button>

        {isLoading || !book ? (
          <div style={{ paddingTop: 40 }}>Loading…</div>
        ) : (
          <>
            {book.needs_review && book.review_reasons?.length ? (
              <div className="banner">
                <span aria-hidden="true">⚠</span>
                <div>
                  <strong>Imported metadata looks suspect</strong>
                  <ul style={{ margin: '4px 0 0', paddingLeft: 18 }}>
                    {book.review_reasons.map((r) => (
                      <li key={r}>{REVIEW_REASONS[r] ?? r}</li>
                    ))}
                  </ul>
                </div>
              </div>
            ) : null}

            <div className="drawer__head">
              <img
                className="drawer__cover"
                src={coverUrl(book.id, 'detail', book.updated_at)}
                alt=""
              />
              <div style={{ minWidth: 0 }}>
                <h2>{book.title}</h2>
                <div className="drawer__byline">
                  {book.authors.map((a, i) => (
                    <span key={a}>
                      {i > 0 && ', '}
                      <a
                        href="#"
                        onClick={(e) => {
                          e.preventDefault()
                          onFilter({ author: a })
                          onClose()
                        }}
                      >
                        {a}
                      </a>
                    </span>
                  ))}
                </div>
                {book.series && (
                  <div style={{ marginBottom: 10 }}>
                    <a
                      href="#"
                      onClick={(e) => {
                        e.preventDefault()
                        onFilter({ series: book.series })
                        onClose()
                      }}
                    >
                      {book.series}
                    </a>
                    {book.series_index != null && ` #${book.series_index}`}
                  </div>
                )}
                <div>
                  {readableFormat(book.files) && (
                    <button
                      className="btn btn--sm"
                      style={{ marginRight: 6, marginBottom: 6 }}
                      onClick={() => onRead(book.id, book.title, readableFormat(book.files)!)}
                    >
                      Read
                    </button>
                  )}
                  {canEdit && (
                    <button
                      className="btn btn--ghost btn--sm"
                      style={{ marginRight: 6, marginBottom: 6 }}
                      onClick={() => onEdit(book.id)}
                    >
                      Edit
                    </button>
                  )}
                  <button
                    className="btn btn--ghost btn--sm"
                    style={{ marginRight: 6, marginBottom: 6 }}
                    onClick={() => setShelfOpen((v) => !v)}
                  >
                    {shelfOpen ? 'Done' : 'Shelves'}
                  </button>
                  {book.files.map((f) => (
                    <a
                      key={f.format}
                      className="btn btn--ghost btn--sm"
                      style={{ marginRight: 6, marginBottom: 6 }}
                      href={`/api/books/${book.id}/download/${f.format.toLowerCase()}`}
                    >
                      {f.format} · {formatBytes(f.size_bytes)}
                    </a>
                  ))}
                  {/* A KEPUB that does not exist yet is still downloadable: the
                      server converts it on the way out, in about a second. The
                      marker is not a warning -- nothing is wrong and nothing
                      needs doing -- it is just so it is visible which books
                      have been converted and which have not. */}
                  {needsKepub(book.files) && (
                    <a
                      className={`btn btn--ghost btn--sm ${book.kepub_ready ? '' : 'btn--derived'}`}
                      style={{ marginRight: 6, marginBottom: 6 }}
                      href={`/api/books/${book.id}/download/kepub`}
                      title={
                        book.kepub_ready
                          ? 'Converted and cached, ready to download.'
                          : 'Not converted yet. Downloading converts it first, which takes about a second.'
                      }
                    >
                      {!book.kepub_ready && (
                        <span className="btn__spark" aria-hidden="true">✦</span>
                      )}
                      KEPUB
                    </a>
                  )}
                </div>
              </div>
            </div>

            {shelfOpen && (
              <AddToShelf bookId={book.id} on={book.shelves} onDone={() => setShelfOpen(false)} />
            )}

            {canEdit && (
              <div style={{ marginTop: 10 }}>
                <button
                  className="btn btn--ghost btn--sm btn--danger"
                  disabled={del.isPending}
                  onClick={() => {
                    if (
                      confirm(
                        `Delete "${book.title}" and its files from disk?\n\n` +
                          'This cannot be undone.',
                      )
                    ) {
                      del.mutate()
                    }
                  }}
                >
                  {del.isPending ? 'Deleting…' : 'Delete this book'}
                </button>
                {del.isError && (
                  <div className="error" style={{ marginTop: 8 }}>
                    {(del.error as Error).message}
                  </div>
                )}
              </div>
            )}

            <dl className="kv">
              {book.publisher && (
                <>
                  <dt>Publisher</dt>
                  <dd>{book.publisher}</dd>
                </>
              )}
              {book.pubdate && (
                <>
                  <dt>Published</dt>
                  <dd>{book.pubdate}</dd>
                </>
              )}
              {book.languages.length > 0 && (
                <>
                  <dt>Language</dt>
                  <dd>{book.languages.join(', ')}</dd>
                </>
              )}
              {book.rating != null && (
                <>
                  <dt>Rating</dt>
                  <dd>{'★'.repeat(Math.round(book.rating / 2))}</dd>
                </>
              )}
              {book.identifiers.length > 0 && (
                <>
                  <dt>Identifiers</dt>
                  <dd>
                    {book.identifiers.map((i) => `${i.scheme}: ${i.value}`).join(', ')}
                  </dd>
                </>
              )}
              {book.shelves?.length ? (
                <>
                  <dt>Shelves</dt>
                  <dd>
                    {book.shelves.map((s) => (
                      <span className="tag" key={s.id}>
                        {s.name}
                        {s.kobo_sync && ' · Kobo'}
                      </span>
                    ))}
                  </dd>
                </>
              ) : null}
              <dt>Added</dt>
              <dd>{new Date(book.added_at).toLocaleDateString('sv-SE')}</dd>
              <dt>Path</dt>
              <dd style={{ fontFamily: 'var(--font-mono)', fontSize: 11, wordBreak: 'break-all' }}>
                {book.path}
              </dd>
            </dl>

            {book.tags.length > 0 && (
              <div style={{ marginTop: 14 }}>
                {book.tags.map((t) => (
                  <button
                    key={t}
                    className="tag"
                    onClick={() => {
                      onFilter({ tag: t })
                      onClose()
                    }}
                  >
                    {t}
                  </button>
                ))}
              </div>
            )}

            {book.description && (
              <div
                className="drawer__desc"
                // Descriptions come from Calibre and contain publisher HTML.
                // They are library-owner-authored content on a same-origin page
                // under a strict CSP that forbids inline and remote script, so
                // the practical risk is formatting, not execution.
                dangerouslySetInnerHTML={{ __html: book.description }}
              />
            )}
          </>
        )}
      </aside>
    </>
  )
}

/** epub.js can only render EPUB; KEPUB is an EPUB underneath but Kobo-specific. */
/** True when a book has an EPUB to convert from and no KEPUB of its own. */
function needsKepub(files: { format: string }[]): boolean {
  return (
    files.some((f) => f.format === 'EPUB') && !files.some((f) => f.format === 'KEPUB')
  )
}

function readableFormat(files: { format: string }[]): string | undefined {
  return files.find((f) => f.format === 'EPUB')?.format
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${Math.round(n / 1024)} kB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}
