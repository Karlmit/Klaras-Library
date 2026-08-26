import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, booksApi, coverUrl, editApi, type Book, type BookEdit, type MetadataResult } from '../api'
import { LookupPicker } from './LookupPicker'

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
  const [lookupOpen, setLookupOpen] = useState(false)
  const [chosen, setChosen] = useState<MetadataResult | null>(null)

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
    })
  }, [book, bookId])

  const save = useMutation({
    mutationFn: (e: BookEdit) => editApi.one(bookId, e),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['book', bookId] })
      void qc.invalidateQueries({ queryKey: ['books'] })
      void qc.invalidateQueries({ queryKey: ['facets'] })
      onClose()
    },
    onError: (e) => setError((e as Error).message),
  })

  if (!book) {
    return (
      <>
        <div className="drawer-backdrop" onClick={onClose} />
        <aside className="drawer">Loading…</aside>
      </>
    )
  }

  const set = <K extends keyof BookEdit>(k: K, v: BookEdit[K]) =>
    setForm((f) => ({ ...f, [k]: v }))

  return (
    <>
      <div className="drawer-backdrop" onClick={onClose} />
      <aside className="drawer" role="dialog" aria-label="Edit book" aria-modal="true">
        <button className="drawer__close" onClick={onClose} aria-label="Close">
          ×
        </button>
        <h2 style={{ marginTop: 0 }}>Edit metadata</h2>

        {error && <div className="error">{error}</div>}

        <CoverSwap bookId={bookId} />

        <button className="btn btn--ghost btn--sm" onClick={() => setLookupOpen((v) => !v)}>
          {lookupOpen ? 'Hide lookup' : 'Look up online…'}
        </button>

        {lookupOpen && !chosen && (
          <Lookup bookId={bookId} onApply={(r) => setChosen(r)} />
        )}

        {lookupOpen && chosen && book && (
          <LookupPicker
            bookId={bookId}
            book={book}
            result={chosen}
            onBack={() => setChosen(null)}
            onApply={(patch: Partial<BookEdit>) => {
              // Into the form, never straight to the database: a provider
              // routinely returns a different edition, and the person editing
              // is the one who can tell.
              setForm((f) => ({ ...f, ...patch }))
              setChosen(null)
              setLookupOpen(false)
              void qc.invalidateQueries({ queryKey: ['book', bookId] })
            }}
          />
        )}

        <form
          style={{ marginTop: 14 }}
          onSubmit={(e) => {
            e.preventDefault()
            save.mutate(form)
          }}
        >
          <Field label="Title">
            <input value={form.title ?? ''} onChange={(e) => set('title', e.target.value)} />
          </Field>
          <Field label="Authors (one per line)">
            <textarea
              rows={3}
              value={(form.authors ?? []).join('\n')}
              onChange={(e) =>
                set('authors', e.target.value.split('\n').map((s) => s.trim()).filter(Boolean))
              }
            />
          </Field>
          <div style={{ display: 'grid', gridTemplateColumns: '2fr 1fr', gap: 10 }}>
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
          <div style={{ display: 'grid', gridTemplateColumns: '2fr 1fr', gap: 10 }}>
            <Field label="Publisher">
              <input
                value={form.publisher ?? ''}
                onChange={(e) => set('publisher', e.target.value)}
              />
            </Field>
            <Field label="Published">
              <input
                type="date"
                value={form.pubdate ?? ''}
                onChange={(e) => set('pubdate', e.target.value)}
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

          <Field label="Rating (0–10, half stars)">
            <input
              type="number" min={0} max={10} step={1}
              value={form.rating ?? ''}
              onChange={(e) =>
                set('rating', e.target.value === '' ? undefined : Number(e.target.value))
              }
            />
          </Field>

          <Field label="Categories (comma separated)">
            <input
              value={(form.tags ?? []).join(', ')}
              onChange={(e) =>
                set('tags', e.target.value.split(',').map((s) => s.trim()).filter(Boolean))
              }
            />
          </Field>
          <Field label="Description">
            <textarea
              rows={6}
              value={form.description ?? ''}
              onChange={(e) => set('description', e.target.value)}
            />
          </Field>

          <p style={{ fontSize: 12, color: 'var(--text-muted)' }}>
            Changing the title, author or series moves this book's files into the
            matching folder. Other books are not touched.
          </p>

          <div style={{ display: 'flex', gap: 8 }}>
            <button className="btn" type="submit" disabled={save.isPending}>
              {save.isPending ? 'Saving…' : 'Save'}
            </button>
            <button className="btn btn--ghost" type="button" onClick={onClose}>
              Cancel
            </button>
          </div>
        </form>
      </aside>
    </>
  )
}

function CoverSwap({ bookId }: { bookId: number }) {
  const qc = useQueryClient()
  const [error, setError] = useState('')
  const [bust, setBust] = useState(0)

  const swap = useMutation({
    mutationFn: (f: File) => booksApi.replaceCover(bookId, f),
    onSuccess: () => {
      setError('')
      // The URL is unchanged, so the browser would keep the old image; a
      // cache-busting parameter is the cheapest way to show the new one.
      setBust(Date.now())
      void qc.invalidateQueries({ queryKey: ['books'] })
    },
    onError: (e) => setError((e as Error).message),
  })

  return (
    <div style={{ display: 'flex', gap: 12, alignItems: 'center', marginBottom: 14 }}>
      <img
        src={`${coverUrl(bookId, 'grid')}${bust ? `?v=${bust}` : ''}`}
        alt=""
        style={{ width: 56, borderRadius: 4, boxShadow: 'var(--shadow-cover)' }}
      />
      <div>
        <label className="btn btn--ghost btn--sm" style={{ cursor: 'pointer' }}>
          {swap.isPending ? 'Uploading…' : 'Replace cover'}
          <input
            type="file"
            accept="image/*"
            hidden
            onChange={(e) => {
              const f = e.target.files?.[0]
              if (f) swap.mutate(f)
            }}
          />
        </label>
        {error && <div className="warn" style={{ marginTop: 4 }}>{error}</div>}
      </div>
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
  const { data, isLoading, error } = useQuery({
    queryKey: ['lookup', bookId],
    queryFn: () => editApi.lookup(bookId),
    staleTime: 5 * 60_000,
  })

  if (isLoading) return <p style={{ color: 'var(--text-muted)' }}>Searching…</p>
  if (error) return <div className="error">{(error as Error).message}</div>
  // ?. on data alone is not enough: results itself can be absent, and reading
  // .length off it is what turned "no matches" into a blank page.
  const results = data?.results ?? []
  if (!results.length) return <p style={{ color: 'var(--text-muted)' }}>Nothing found.</p>

  return (
    <div className="lookup">
      {results.slice(0, 6).map((r, i) => (
        <button key={i} className="lookup__item" onClick={() => onApply(r)}>
          {r.cover_url && <img src={r.cover_url} alt="" loading="lazy" />}
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
