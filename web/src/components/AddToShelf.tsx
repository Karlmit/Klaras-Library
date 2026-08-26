import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { shelvesApi, type ShelfRef } from '../api'

/**
 * Which shelves a book is on, and a way to change that.
 *
 * Previously add-only, which made shelves a one-way door: a book put on the
 * wrong one, or kept from the discovery screen and later thought better of,
 * could not be taken off from anywhere in the interface. The server has always
 * supported removal -- including the tombstone a Kobo needs in order to drop
 * the book -- so this was a gap in the UI, not the model.
 *
 * Only shelves the reader owns can be toggled. A public shelf someone else owns
 * shows the book's membership but is not theirs to edit.
 */
export function AddToShelf({
  bookId, on, onDone,
}: {
  bookId: number
  /** Shelves this book is already on, from the book detail. */
  on?: ShelfRef[]
  onDone: () => void
}) {
  const qc = useQueryClient()
  const { data } = useQuery({ queryKey: ['shelves'], queryFn: shelvesApi.list })
  const [error, setError] = useState('')
  const [busyId, setBusyId] = useState<number | null>(null)
  const current = new Set((on ?? []).map((s) => s.id))

  const toggle = useMutation({
    mutationFn: ({ shelfId, isOn }: { shelfId: number; isOn: boolean }) =>
      isOn ? shelvesApi.setBooks(shelfId, [], [bookId]) : shelvesApi.setBooks(shelfId, [bookId], []),
    onMutate: (v) => setBusyId(v.shelfId),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['book', bookId] })
      void qc.invalidateQueries({ queryKey: ['shelves'] })
      void qc.invalidateQueries({ queryKey: ['books'] })
      setError('')
    },
    onError: (e) => setError((e as Error).message),
    onSettled: () => setBusyId(null),
  })

  const mine = data?.shelves?.filter((s) => s.mine) ?? []
  if (!mine.length) {
    return <p className="hint">No shelves yet. Create one under Settings → Shelves.</p>
  }

  return (
    <div className="shelfpick">
      {error && <div className="error">{error}</div>}
      {mine.map((s) => {
        const isOn = current.has(s.id)
        return (
          <button
            key={s.id}
            className={`shelfpick__item ${isOn ? 'shelfpick__item--on' : ''}`}
            aria-pressed={isOn}
            disabled={busyId === s.id}
            onClick={() => toggle.mutate({ shelfId: s.id, isOn })}
            title={isOn ? `Remove from ${s.name}` : `Add to ${s.name}`}
          >
            <span className="shelfpick__tick" aria-hidden="true">{isOn ? '✓' : ''}</span>
            {s.kobo_sync && <span className="kobo-dot" aria-label="Syncs to Kobo" />}
            {s.name}
            <span className="sub">{s.book_count}</span>
          </button>
        )
      })}
      <button className="btn btn--sm btn--ghost shelfpick__done" onClick={onDone}>Done</button>
    </div>
  )
}
