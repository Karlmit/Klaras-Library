import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, booksApi, coverUrl, editApi, remoteImage, type Book, type BookEdit, type MetadataResult } from '../api'
import { LookupPicker } from './LookupPicker'
import { CoverFinder } from './CoverFinder'

interface Props {
  bookId: number
  onClose: () => void
}

/** Single-book metadata editor, with optional lookup from external providers. */
export function EditPanel({ bookId, onClose }: Props) {
  const qc = useQueryClient()
  const { data: book } = useQuery({ queryKey: ['book', bookId], queryFn: () => api.book(bookId) })

  const [form, setForm] = useState<BookEdit>({})
  const [error, setError] = useState('')
  const [chosen, setChosen] = useState<MetadataResult | null>(null)
  const [tab, setTab] = useState<TabKey>('details')

  // Seed the form from the book once per book, not on every change to it.
  // Fetching a cover invalidates this query on purpose (so the thumbnail
  // refreshes), and re-seeding on the refetch silently threw away the fields
  // the person had just picked in the lookup panel.
  const seeded = useRef<number | null>(null)
  useEffect(() => {
    if (!book) return
    if (seeded.current === bookId) return
    seeded.current = bookId
    setForm({
      title: book.title,
      authors: book.authors,
      series: book.series ?? '',
      series_index: book.series_index,
      publisher: book.publisher ?? '',
      pubdate: book.pubdate ?? '',
      description: book.description ?? '',
      tags: book.tags,
      languages: book.languages,
      rating: book.rating,
      isbn: (book.identifiers ?? []).find((i) => i.scheme === 'isbn')?.value ?? '',
    })
  }, [book, bookId])

  // Save and Save & Close are different intentions, so closing is the caller's
  // decision rather than something the mutation always does.
  const closeAfter = useRef(false)
  const [saved, setSaved] = useState(false)
  const save = useMutation({
    mutationFn: (e: BookEdit) => editApi.one(bookId, e),
    onSuccess: () => {
      setError('')
      void qc.invalidateQueries({ queryKey: ['book', bookId] })
      void qc.invalidateQueries({ queryKey: ['books'] })
      void qc.invalidateQueries({ queryKey: ['facets'] })
      if (closeAfter.current) {
        onClose()
        return
      }
      setSaved(true)
      window.setTimeout(() => setSaved(false), 2500)
    },
    onError: (e) => setError((e as Error).message),
  })

  const commit = (andClose: boolean) => {
    closeAfter.current = andClose
    save.mutate(form)
  }

  // Escape closes, as it does for every other overlay here.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  if (!book) {
    return (
      <>
        <div className="emodal-backdrop" onClick={onClose} />
        <div className="emodal emodal--loading">Loading…</div>
      </>
    )
  }

  const set = <K extends keyof BookEdit>(k: K, v: BookEdit[K]) =>
    setForm((f) => ({ ...f, [k]: v }))

  return (
    <>
      <div className="emodal-backdrop" onClick={onClose} />
      <div className="emodal" role="dialog" aria-modal="true" aria-label={`Edit ${book.title}`}>
        <header className="emodal__head">
          <div className="emodal__who">
            <h2>{book.title}</h2>
            <p>{(book.authors ?? []).join(', ')}</p>
          </div>
          <button className="emodal__close" onClick={onClose} aria-label="Close">
            ×
          </button>
        </header>

        <nav className="emodal__tabs" role="tablist">
          {TABS.map((t) => (
            <button
              key={t.key}
              role="tab"
              aria-selected={tab === t.key}
              className={`emodal__tab ${tab === t.key ? 'emodal__tab--on' : ''}`}
              onClick={() => setTab(t.key)}
            >
              {t.label}
            </button>
          ))}
        </nav>

        <div className="emodal__body">
          {error && <div className="error">{error}</div>}

          {tab === 'details' && (
            <form
              className="eform"
              onSubmit={(e) => {
                e.preventDefault()
                commit(false)
              }}
            >
              <div className="eform__grid">
                <Field label="Title">
                  <input value={form.title ?? ''} onChange={(e) => set('title', e.target.value)} />
                </Field>
                <Field label="Published">
                  <input
                    type="date"
                    value={form.pubdate ?? ''}
                    onChange={(e) => set('pubdate', e.target.value)}
                  />
                </Field>
              </div>

              <Field label="Authors (one per line)">
                <textarea
                  rows={3}
                  value={(form.authors ?? []).join('\n')}
                  onChange={(e) =>
                    set('authors', e.target.value.split('\n').map((s) => s.trim()).filter(Boolean))
                  }
                />
              </Field>

              <div className="eform__grid eform__grid--series">
                <Field label="Series">
                  <input value={form.series ?? ''} onChange={(e) => set('series', e.target.value)} />
                </Field>
                <Field label="Number">
                  <input
                    type="number"
                    step="0.1"
                    value={form.series_index ?? ''}
                    onChange={(e) =>
                      set('series_index', e.target.value === '' ? undefined : Number(e.target.value))
                    }
                  />
                </Field>
              </div>

              <Field label="Description">
                <textarea
                  rows={7}
                  value={form.description ?? ''}
                  onChange={(e) => set('description', e.target.value)}
                />
              </Field>

              <div className="eform__grid">
                <Field label="Publisher">
                  <input
                    value={form.publisher ?? ''}
                    onChange={(e) => set('publisher', e.target.value)}
                  />
                </Field>
                <Field label="ISBN">
                  <input
                    value={form.isbn ?? ''}
                    placeholder="9789100138813"
                    spellCheck={false}
                    onChange={(e) => set('isbn', e.target.value)}
                  />
                </Field>
              </div>

              <div className="eform__grid">
                <Field label="Categories (comma separated)">
                  <input
                    value={(form.tags ?? []).join(', ')}
                    onChange={(e) =>
                      set('tags', e.target.value.split(',').map((s) => s.trim()).filter(Boolean))
                    }
                  />
                </Field>
                <Field label="Rating (0–10, half stars)">
                  <input
                    type="number" min={0} max={10} step={1}
                    value={form.rating ?? ''}
                    onChange={(e) =>
                      set('rating', e.target.value === '' ? undefined : Number(e.target.value))
                    }
                  />
                </Field>
              </div>

              <Field label="Languages (comma separated, three-letter codes)">
                <input
                  value={(form.languages ?? []).join(', ')}
                  onChange={(e) =>
                    set(
                      'languages',
                      e.target.value.split(',').map((s) => s.trim().toLowerCase()).filter(Boolean),
                    )
                  }
                  placeholder="swe, eng"
                />
                <p className="hint" style={{ margin: '4px 0 0' }}>
                  ISO 639-2 codes: swe, eng, dan, nor, deu, fra, ara. These are what the
                  Languages filter groups by, and what a Kobo is told about the book.
                </p>
              </Field>

              {/* Submitting with Enter should save, and a form needs a submit
                  button for that to work even when the visible one is in the
                  footer outside it. */}
              <button type="submit" hidden />
            </form>
          )}

          {tab === 'cover' && <CoverSwap bookId={bookId} />}

          {tab === 'match' && (
            <>
              {!chosen && <Lookup bookId={bookId} onApply={(r) => setChosen(r)} />}
              {chosen && (
                <LookupPicker
                  bookId={bookId}
                  book={book}
                  result={chosen}
                  onBack={() => setChosen(null)}
                  onApply={(patch: Partial<BookEdit>) => {
                    // Into the form, never straight to the database: a provider
                    // routinely returns a different edition, and the person
                    // editing is the one who can tell. Landing on Details makes
                    // that visible rather than leaving it to be discovered.
                    setForm((f) => ({ ...f, ...patch }))
                    setChosen(null)
                    setTab('details')
                    void qc.invalidateQueries({ queryKey: ['book', bookId] })
                  }}
                />
              )}
            </>
          )}

          {tab === 'files' && <FilesTab book={book} />}
        </div>

        <footer className="emodal__foot">
          <span className="hint">
            {saved
              ? 'Saved.'
              : 'Changing the title, author or series moves this book\u2019s files into the matching folder. Other books are not touched.'}
          </span>
          <div className="emodal__actions">
            <button className="btn btn--ghost" type="button" onClick={onClose}>
              Cancel
            </button>
            <button className="btn btn--ghost" type="button" disabled={save.isPending}
                    onClick={() => commit(false)}>
              {save.isPending && !closeAfter.current ? 'Saving…' : 'Save'}
            </button>
            <button className="btn" type="button" disabled={save.isPending}
                    onClick={() => commit(true)}>
              Save & Close
            </button>
          </div>
        </footer>
      </div>
    </>
  )
}

const TABS = [
  { key: 'details', label: 'Details' },
  { key: 'cover', label: 'Cover' },
  { key: 'match', label: 'Match' },
  { key: 'files', label: 'Files' },
] as const

type TabKey = (typeof TABS)[number]['key']

/** What is actually on disk for this book. Read-only: this is for checking. */
function FilesTab({ book }: { book: Book }) {
  const mb = (n: number) => `${(n / 1024 / 1024).toFixed(1)} MB`
  return (
    <div className="files">
      <Field label="Folder">
        <input value={book.path} readOnly spellCheck={false} />
      </Field>
      <div className="table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th>Format</th>
              <th>File</th>
              <th style={{ textAlign: 'right' }}>Size</th>
            </tr>
          </thead>
          <tbody>
            {(book.files ?? []).map((f) => (
              <tr key={f.filename}>
                <td>{f.format}</td>
                <td style={{ wordBreak: 'break-all' }}>{f.filename}</td>
                <td style={{ textAlign: 'right', fontVariantNumeric: 'tabular-nums' }}>
                  {mb(f.size_bytes)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {!(book.files ?? []).length && (
        <p style={{ color: 'var(--text-muted)' }}>No files recorded for this book.</p>
      )}
    </div>
  )
}

function CoverSwap({ bookId }: { bookId: number }) {
  const qc = useQueryClient()
  const [error, setError] = useState('')
  const [bust, setBust] = useState(0)
  const [url, setUrl] = useState('')
  const [finding, setFinding] = useState(false)

  // Both routes end the same way. The grid has to be invalidated too, or the
  // card behind this panel keeps the old picture until something else refetches
  // it -- which is how a replaced cover appeared not to have worked at all.
  const replaced = () => {
    setError('')
    setBust(Date.now())
    void qc.invalidateQueries({ queryKey: ['books'] })
    void qc.invalidateQueries({ queryKey: ['book', bookId] })
  }

  const swap = useMutation({
    mutationFn: (f: File) => booksApi.replaceCover(bookId, f),
    onSuccess: replaced,
    onError: (e) => setError((e as Error).message),
  })

  const fromUrl = useMutation({
    mutationFn: (u: string) => booksApi.fetchCover(bookId, u),
    onSuccess: () => {
      setUrl('')
      setFinding(false)
      replaced()
    },
    onError: (e) => setError((e as Error).message),
  })

  const busy = swap.isPending || fromUrl.isPending

  return (
    <div className="coverswap">
      <img
        src={`${coverUrl(bookId, 'grid')}${bust ? `?v=${bust}` : ''}`}
        alt=""
        className="coverswap__img"
      />
      <div className="coverswap__ctl">
        <div className="coverswap__row">
          <label className="btn btn--ghost btn--sm" style={{ cursor: busy ? 'default' : 'pointer' }}>
            {swap.isPending ? 'Uploading…' : 'Upload a file'}
            <input
              type="file"
              accept="image/*"
              hidden
              disabled={busy}
              onChange={(e) => {
                const f = e.target.files?.[0]
                if (f) swap.mutate(f)
              }}
            />
          </label>
          <span className="coverswap__or">or</span>
        </div>
        <form
          className="coverswap__row"
          onSubmit={(e) => {
            e.preventDefault()
            const u = url.trim()
            if (u) fromUrl.mutate(u)
          }}
        >
          <input
            type="url"
            className="coverswap__url"
            placeholder="paste an image address"
            value={url}
            disabled={busy}
            onChange={(e) => setUrl(e.target.value)}
          />
          <button className="btn btn--sm" disabled={busy || !url.trim()}>
            {fromUrl.isPending ? 'Fetching…' : 'Fetch'}
          </button>
        </form>
        <div className="coverswap__row">
          <button
            type="button"
            className="btn btn--ghost btn--sm"
            disabled={busy}
            onClick={() => setFinding((f) => !f)}
          >
            {finding ? 'Hide covers' : 'Find covers online'}
          </button>
        </div>
        <p className="hint coverswap__hint">
          The server downloads it, so the picture never has to reach your computer first.
        </p>
        {error && <div className="warn" style={{ marginTop: 4 }}>{error}</div>}
      </div>
      {finding && (
        <CoverFinder
          bookId={bookId}
          busy={fromUrl.isPending}
          onClose={() => setFinding(false)}
          onUse={(u) => fromUrl.mutate(u)}
        />
      )}
    </div>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="field">
      <label>{label}</label>
      {children}
    </div>
  )
}

function Lookup({
  bookId,
  onApply,
}: {
  bookId: number
  onApply: (r: MetadataResult) => void
}) {
  // What was actually searched for, so it can be changed. The first search runs
  // on what the book already holds; when that finds nothing -- and for a Swedish
  // title carrying a subtitle, it often does -- the fix is almost always a
  // shorter title, not a different provider.
  const [terms, setTerms] = useState<{ title: string; author: string; isbn: string } | null>(null)
  const [provider, setProvider] = useState('')
  const [run, setRun] = useState(0)

  const { data, isLoading, error } = useQuery({
    queryKey: ['lookup', bookId, run],
    queryFn: () => editApi.lookup(bookId, { ...(terms ?? {}), provider: provider || undefined }),
    staleTime: 5 * 60_000,
  })

  // Prefilled from the book on the first reply, then left alone: retyping is the
  // whole point, and overwriting what someone typed on every refetch defeats it.
  useEffect(() => {
    if (data?.query && terms === null) setTerms(data.query)
  }, [data, terms])

  const results = data?.results ?? []
  const search = (e: React.FormEvent) => {
    e.preventDefault()
    setRun((n) => n + 1)
  }

  return (
    <div className="lookup">
      <form className="lookup__terms" onSubmit={search}>
        <input
          value={terms?.title ?? ''}
          placeholder="title"
          onChange={(e) => setTerms((t) => ({ ...(t ?? { title: '', author: '', isbn: '' }), title: e.target.value }))}
        />
        <input
          value={terms?.author ?? ''}
          placeholder="author"
          onChange={(e) => setTerms((t) => ({ ...(t ?? { title: '', author: '', isbn: '' }), author: e.target.value }))}
        />
        <input
          value={terms?.isbn ?? ''}
          placeholder="ISBN"
          className="lookup__isbn"
          onChange={(e) => setTerms((t) => ({ ...(t ?? { title: '', author: '', isbn: '' }), isbn: e.target.value }))}
        />
        <select value={provider} onChange={(e) => setProvider(e.target.value)}>
          <option value="">All sources</option>
          {(data?.providers ?? []).map((n) => (
            <option key={n} value={n}>
              {n} only
            </option>
          ))}
        </select>
        <button className="btn btn--sm" disabled={isLoading}>
          {isLoading ? 'Searching…' : 'Search'}
        </button>
      </form>

      {isLoading && <p style={{ color: 'var(--text-muted)' }}>Searching…</p>}
      {!!error && <div className="error">{(error as Error).message}</div>}
      {/* A source that failed is not a source that found nothing, and the
          difference decides whether retrying is worth anything. */}
      {(data?.sources ?? [])
        .filter((s) => s.error)
        .map((s) => (
          <div key={s.name} className="warn" style={{ marginBottom: 8 }}>
            {s.name}: {s.error}
          </div>
        ))}

      {!isLoading && !error && !results.length && (
        <p style={{ color: 'var(--text-muted)' }}>
          Nothing found. A shorter title usually helps — try dropping the subtitle.
        </p>
      )}

      {results.slice(0, 6).map((r, i) => (
        <button key={i} className="lookup__item" onClick={() => onApply(r)}>
          {r.cover_url && <img src={remoteImage(r.cover_url)} alt="" loading="lazy" />}
          <div style={{ minWidth: 0 }}>
            <strong>{r.title}</strong>
            <div style={{ color: 'var(--text-muted)', fontSize: 12 }}>
              {(r.authors ?? []).join(', ')}
              {r.pubdate && ` · ${r.pubdate.slice(0, 4)}`}
            </div>
            <div style={{ fontSize: 11, color: 'var(--v-600)' }}>{r.source}</div>
          </div>
        </button>
      ))}
    </div>
  )
}

/** Book is referenced only for its type. */
export type { Book }
