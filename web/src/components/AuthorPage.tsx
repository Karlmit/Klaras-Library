import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { browseApi, portraitUrl } from '../api'

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
}: {
  authorId: number
  onBooks: (name: string) => void
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
    </div>
  )
}
