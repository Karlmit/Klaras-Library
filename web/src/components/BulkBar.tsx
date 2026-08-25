import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { editApi, shelvesApi, type BookEdit } from '../api'

interface Props {
  selected: Set<number>
  onClear: () => void
}

/**
 * Actions for a multi-selection.
 *
 * calibre-web has no bulk metadata edit at all, which is the single biggest
 * gap for a library this size: 1,115 books here carry a merged author name and
 * fixing those one at a time is not realistic.
 */
export function BulkBar({ selected, onClear }: Props) {
  const qc = useQueryClient()
  const ids = [...selected]
  const [panel, setPanel] = useState<'none' | 'tags' | 'author' | 'shelf'>('none')
  const [error, setError] = useState('')

  const { data: shelves } = useQuery({ queryKey: ['shelves'], queryFn: shelvesApi.list })

  const done = () => {
    setPanel('none')
    setError('')
    void qc.invalidateQueries({ queryKey: ['books'] })
    void qc.invalidateQueries({ queryKey: ['facets'] })
    void qc.invalidateQueries({ queryKey: ['shelves'] })
    onClear()
  }

  const bulk = useMutation({
    mutationFn: (v: { edit: BookEdit; add?: string[]; remove?: string[] }) =>
      editApi.bulk(ids, v.edit, v.add, v.remove),
    onSuccess: done,
    onError: (e) => setError((e as Error).message),
  })

  const toShelf = useMutation({
    mutationFn: (shelfId: number) => shelvesApi.setBooks(shelfId, ids, []),
    onSuccess: done,
    onError: (e) => setError((e as Error).message),
  })

  if (ids.length === 0) return null

  return (
    <div className="bulkbar">
      <strong>{ids.length.toLocaleString('sv-SE')} selected</strong>

      {error && <span className="bulkbar__error">{error}</span>}

      {panel === 'none' && (
        <>
          <button className="btn btn--sm btn--ghost" onClick={() => setPanel('tags')}>
            Add category
          </button>
          <button className="btn btn--sm btn--ghost" onClick={() => setPanel('author')}>
            Set author
          </button>
          <button className="btn btn--sm btn--ghost" onClick={() => setPanel('shelf')}>
            Add to shelf
          </button>
          <button
            className="btn btn--sm btn--ghost"
            onClick={() => bulk.mutate({ edit: { needs_review: false } })}
            title="Clear the review flag on the selected books"
          >
            Mark reviewed
          </button>
        </>
      )}

      {panel === 'tags' && (
        <InlineForm
          label="Category to add"
          busy={bulk.isPending}
          onCancel={() => setPanel('none')}
          onSubmit={(v) => bulk.mutate({ edit: {}, add: [v] })}
        />
      )}

      {panel === 'author' && (
        <InlineForm
          label="Replace author with"
          hint="This also moves the files into the new author's folder."
          busy={bulk.isPending}
          onCancel={() => setPanel('none')}
          onSubmit={(v) => bulk.mutate({ edit: { authors: [v] } })}
        />
      )}

      {panel === 'shelf' && (
        <>
          <select
            className="sort"
            defaultValue=""
            onChange={(e) => {
              if (e.target.value) toShelf.mutate(Number(e.target.value))
            }}
          >
            <option value="" disabled>
              Choose a shelf…
            </option>
            {shelves?.shelves
              .filter((s) => s.mine)
              .map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name}
                  {s.kobo_sync ? ' (Kobo)' : ''}
                </option>
              ))}
          </select>
          <button className="btn btn--sm btn--ghost" onClick={() => setPanel('none')}>
            Cancel
          </button>
        </>
      )}

      <div style={{ flex: 1 }} />
      <button className="btn btn--sm btn--ghost" onClick={onClear}>
        Clear selection
      </button>
    </div>
  )
}

function InlineForm({
  label,
  hint,
  busy,
  onSubmit,
  onCancel,
}: {
  label: string
  hint?: string
  busy: boolean
  onSubmit: (value: string) => void
  onCancel: () => void
}) {
  const [value, setValue] = useState('')
  return (
    <form
      style={{ display: 'flex', gap: 8, alignItems: 'center' }}
      onSubmit={(e) => {
        e.preventDefault()
        if (value.trim()) onSubmit(value.trim())
      }}
    >
      <label className="visually-hidden" htmlFor="bulkval">
        {label}
      </label>
      <input
        id="bulkval"
        autoFocus
        placeholder={label}
        value={value}
        onChange={(e) => setValue(e.target.value)}
        style={{
          height: 28,
          padding: '0 8px',
          border: '1px solid var(--border-strong)',
          borderRadius: 'var(--radius-sm)',
          minWidth: 220,
        }}
      />
      {hint && <span style={{ fontSize: 12, color: 'var(--text-muted)' }}>{hint}</span>}
      <button className="btn btn--sm" disabled={busy || !value.trim()} type="submit">
        {busy ? 'Applying…' : 'Apply'}
      </button>
      <button className="btn btn--sm btn--ghost" type="button" onClick={onCancel}>
        Cancel
      </button>
    </form>
  )
}
