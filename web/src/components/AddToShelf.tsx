import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { shelvesApi } from '../api'

/**
 * Put one book on a shelf, from the book itself.
 *
 * The bulk bar can do this for a selection, but reaching for multi-select to
 * shelve a single book you are already looking at is the wrong shape.
 */
export function AddToShelf({ bookId, onDone }: { bookId: number; onDone: () => void }) {
  const qc = useQueryClient()
  const { data } = useQuery({ queryKey: ['shelves'], queryFn: shelvesApi.list })
  const [error, setError] = useState('')

  const add = useMutation({
    mutationFn: (shelfId: number) => shelvesApi.setBooks(shelfId, [bookId], []),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['book', bookId] })
      void qc.invalidateQueries({ queryKey: ['shelves'] })
      onDone()
    },
    onError: (e) => setError((e as Error).message),
  })

  const mine = data?.shelves.filter((s) => s.mine) ?? []
  if (!mine.length) {
    return <p className="hint">No shelves yet. Create one under Settings → Shelves.</p>
  }

  return (
    <div className="shelfpick">
      {error && <div className="error">{error}</div>}
      {mine.map((s) => (
        <button key={s.id} className="shelfpick__item" onClick={() => add.mutate(s.id)}>
          {s.kobo_sync && <span className="kobo-dot" aria-label="Syncs to Kobo" />}
          {s.name}
          <span className="sub">{s.book_count}</span>
        </button>
      ))}
    </div>
  )
}
