import { useEffect, useState } from 'react'
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, browseApi, coverUrl, portraitUrl, type BookListItem } from '../api'

/**
 * One author: their picture, and what to do about it.
 *
 * The background sweep finds a portrait for the well-known names and nothing
 * for most of the rest, which is fine until it is your library and you know
 * perfectly well what the author looks like. So a picture can be uploaded, or
 * taken from an address, or looked up again after a misspelled name has been
 * corrected, or removed when the sweep found the wrong person.
 */
export function AuthorPage({
  authorId,
  onBooks,
  onOpenBook,
  onName,
}: {
  authorId: number
  onBooks: (name: string) => void
  onOpenBook: (id: number) => void
  /** Reported upward so the breadcrumb can say the name rather than "this author". */
  onName?: (name: string) => void
}) {
  const qc = useQueryClient()
  const { data: author, isLoading } = useQuery({
    queryKey: ['author', authorId],
    queryFn: () => browseApi.author(authorId),
  })
  const [url, setUrl] = useState('')
  const [error, setError] = useState('')
  const [bust, setBust] = useState(0)

  const done = () => {
    setError('')
    setUrl('')
    setBust(Date.now())
    void qc.invalidateQueries({ queryKey: ['author', authorId] })
    void qc.invalidateQueries({ queryKey: ['authors'] })
  }
  const fail = (e: unknown) => setError((e as Error).message)

  const up = useMutation({
    mutationFn: (f: File) => browseApi.setPortrait(authorId, f),
    onSuccess: done, onError: fail,
  })
  const fromUrl = useMutation({
    mutationFn: (u: string) => browseApi.fetchPortrait(authorId, u),
    onSuccess: done, onError: fail,
  })
  const lookUp = useMutation({
    mutationFn: () => browseApi.lookUpPortrait(authorId),
    onSuccess: done, onError: fail,
  })
  const clear = useMutation({
    mutationFn: () => browseApi.clearPortrait(authorId),
    onSuccess: done, onError: fail,
  })

  const busy = up.isPending || fromUrl.isPending || lookUp.isPending || clear.isPending

  useEffect(() => {
    if (author?.name) onName?.(author.name)
  }, [author?.name, onName])

  if (isLoading || !author) return <p className="browse__empty">Loading…</p>

  return (
    <div className="browse">
      <div className="apage">
        <div className="apage__face">
          {author.has_portrait ? (
            <img src={`${portraitUrl(author.id)}${bust ? `?v=${bust}` : ''}`} alt="" />
          ) : (
            <span className="apage__none">no picture</span>
          )}
        </div>

        <div className="apage__meta">
          <h1>{author.name}</h1>
          <p className="apage__count">
            <button className="linkish" onClick={() => onBooks(author.name)}>
              {author.books === 1 ? '1 book' : `${author.books} books`} in the library →
            </button>
          </p>
          {author.has_portrait && author.portrait_from && (
            <p className="hint">Picture from {author.portrait_from}.</p>
          )}
          {!author.has_portrait && (
            <p className="hint">
              {author.portrait_tried
                ? 'Nothing was found for this name.'
                : 'Not looked up yet — the sweep works through the most-published authors first.'}
            </p>
          )}

          {error && <div className="warn" style={{ marginTop: 8 }}>{error}</div>}

          <div className="apage__actions">
            <label className="btn btn--ghost btn--sm" style={{ cursor: busy ? 'default' : 'pointer' }}>
              {up.isPending ? 'Uploading…' : 'Upload a picture'}
              <input
                type="file" accept="image/*" hidden disabled={busy}
                onChange={(e) => {
                  const f = e.target.files?.[0]
                  if (f) up.mutate(f)
                }}
              />
            </label>
            <button
              className="btn btn--ghost btn--sm"
              disabled={busy}
              onClick={() => lookUp.mutate()}
            >
              {lookUp.isPending ? 'Searching…' : 'Search Wikidata again'}
            </button>
            {author.has_portrait && (
              <button className="btn btn--ghost btn--sm" disabled={busy} onClick={() => clear.mutate()}>
                Remove picture
              </button>
            )}
          </div>

          <form
            className="apage__url"
            onSubmit={(e) => {
              e.preventDefault()
              const u = url.trim()
              if (u) fromUrl.mutate(u)
            }}
          >
            <input
              type="url"
              placeholder="or paste an image address"
              value={url}
              disabled={busy}
              onChange={(e) => setUrl(e.target.value)}
            />
            <button className="btn btn--sm" disabled={busy || !url.trim()}>
              {fromUrl.isPending ? 'Fetching…' : 'Fetch'}
            </button>
          </form>
        </div>
      </div>

      <AuthorBooks name={author.name} onOpenBook={onOpenBook} onBooks={onBooks} />
    </div>
  )
}

/**
 * This author's books, listed on their page.
 *
 * Not the virtualised grid: that owns its own scroller, and nesting one inside
 * a page that already scrolls gives two scrollbars and a header that will not
 * move. An author has at most a few dozen books here, so a plain grid that
 * scrolls with the page is both simpler and better behaved.
 */
function AuthorBooks({
  name,
  onOpenBook,
  onBooks,
}: {
  name: string
  onOpenBook: (id: number) => void
  onBooks: (name: string) => void
}) {
  const { data, fetchNextPage, hasNextPage, isFetchingNextPage, isLoading } = useInfiniteQuery({
    queryKey: ['author-books', name],
    initialPageParam: undefined as string | undefined,
    // Series first, so a series reads in order rather than alphabetically.
    queryFn: ({ pageParam }) =>
      api.books({ author: name, sort: 'series', limit: 100, cursor: pageParam }),
    getNextPageParam: (last) => last.next_cursor,
  })
  const books: BookListItem[] = data?.pages.flatMap((p) => p.items) ?? []

  if (isLoading) return <p className="browse__empty">Loading books…</p>
  if (!books.length) return null

  return (
    <section className="abooks">
      <div className="abooks__head">
        <h2>Books</h2>
        <button className="linkish" onClick={() => onBooks(name)}>
          Open in the library →
        </button>
      </div>
      <div className="abooks__grid">
        {books.map((b) => (
          <button key={b.id} className="abook" onClick={() => onOpenBook(b.id)} title={b.title}>
            {b.has_cover ? (
              <img src={coverUrl(b.id, 'grid', b.cover_v)} alt="" loading="lazy" />
            ) : (
              <span className="abook__nocover">no cover</span>
            )}
            <span className="abook__title">{b.title}</span>
            {b.series && (
              <span className="abook__series">
                {b.series}
                {b.series_index != null && ` #${b.series_index}`}
              </span>
            )}
          </button>
        ))}
      </div>
      {hasNextPage && (
        <button
          className="btn btn--ghost btn--sm"
          style={{ marginTop: 12 }}
          disabled={isFetchingNextPage}
          onClick={() => void fetchNextPage()}
        >
          {isFetchingNextPage ? 'Loading…' : 'Show more'}
        </button>
      )}
    </section>
  )
}
