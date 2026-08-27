import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, browseApi } from '../api'

/**
 * Tidy the category list by merging names that mean the same thing.
 *
 * The library has over a thousand categories and many are duplicates of each
 * other -- "F" on 6,709 books and "Fiction" on 304, which are the same thing
 * written twice. Fixing that by selecting the books and editing them is the
 * long way round and only ever adds: the category itself is what is wrong, so
 * this renames the category and lets the books follow.
 */
export function CategoryMerge({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient()
  const { data } = useQuery({
    queryKey: ['facets', 'all-tags'],
    // The sidebar asks for the top 50; merging needs the whole list.
    queryFn: () => api.facets(2000),
    staleTime: 60_000,
  })
  const [q, setQ] = useState('')
  const [picked, setPicked] = useState<Set<string>>(new Set())
  const [target, setTarget] = useState('')
  const [done, setDone] = useState('')
  const [error, setError] = useState('')

  const tags = useMemo(() => {
    const all = data?.tags ?? []
    const needle = q.trim().toLowerCase()
    return needle ? all.filter((t) => t.value.toLowerCase().includes(needle)) : all
  }, [data, q])

  const merge = useMutation({
    mutationFn: () => browseApi.mergeTags([...picked], target.trim()),
    onSuccess: (r) => {
      setDone(
        target.trim()
          ? `${r.books.toLocaleString('sv-SE')} books moved to “${target.trim()}”.`
          : `Removed from ${r.books.toLocaleString('sv-SE')} books.`,
      )
      setPicked(new Set())
      setTarget('')
      setError('')
      void qc.invalidateQueries({ queryKey: ['facets'] })
      void qc.invalidateQueries({ queryKey: ['books'] })
    },
    onError: (e) => setError((e as Error).message),
  })

  const toggle = (name: string) =>
    setPicked((p) => {
      const n = new Set(p)
      n.has(name) ? n.delete(name) : n.add(name)
      return n
    })

  const affected = tags
    .filter((t) => picked.has(t.value))
    .reduce((n, t) => n + t.count, 0)

  return (
    <>
      <div className="emodal-backdrop" onClick={onClose} />
      <div className="emodal" role="dialog" aria-modal="true" aria-label="Merge categories">
        <header className="emodal__head">
          <div className="emodal__who">
            <h2>Categories</h2>
            <p>Pick the ones that mean the same thing, then give them one name.</p>
          </div>
          <button className="emodal__close" onClick={onClose} aria-label="Close">×</button>
        </header>

        <div className="emodal__body">
          {error && <div className="error">{error}</div>}
          {done && <div className="notice">{done}</div>}

          <input
            className="browse__find"
            style={{ width: '100%', marginBottom: 10 }}
            placeholder="Find a category…"
            value={q}
            onChange={(e) => setQ(e.target.value)}
          />

          <div className="tagpick">
            {tags.map((t) => (
              <label key={t.value} className={`tagpick__item ${picked.has(t.value) ? 'tagpick__item--on' : ''}`}>
                <input
                  type="checkbox"
                  checked={picked.has(t.value)}
                  onChange={() => toggle(t.value)}
                />
                <span className="tagpick__name">{t.value}</span>
                <span className="tagpick__count">{t.count.toLocaleString('sv-SE')}</span>
              </label>
            ))}
            {!tags.length && <p className="browse__empty">No category matches that.</p>}
          </div>
        </div>

        <footer className="emodal__foot">
          <span className="hint">
            {picked.size
              ? `${picked.size} selected, ${affected.toLocaleString('sv-SE')} books affected.`
              : 'Nothing selected.'}
          </span>
          <div className="emodal__actions">
            <input
              className="pick__input"
              style={{ width: '13rem' }}
              placeholder="New name for them"
              value={target}
              onChange={(e) => setTarget(e.target.value)}
            />
            <button
              className="btn"
              disabled={!picked.size || !target.trim() || merge.isPending}
              onClick={() => merge.mutate()}
            >
              {merge.isPending ? 'Merging…' : 'Rename'}
            </button>
          </div>
        </footer>
      </div>
    </>
  )
}
