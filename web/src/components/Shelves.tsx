import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { shelvesApi, type Shelf } from '../api'

/**
 * Shelf management.
 *
 * The Kobo toggle is the important control here: a shelf marked for sync is
 * exactly what a device sees as a Collection, and it is the only thing that
 * decides which books leave the library. Everything else on this screen is
 * housekeeping around that one checkbox.
 */
export function Shelves({ onBrowse }: { onBrowse: (shelfId: number) => void }) {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({ queryKey: ['shelves'], queryFn: shelvesApi.list })
  const [newName, setNewName] = useState('')
  const [error, setError] = useState('')

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ['shelves'] })
    void qc.invalidateQueries({ queryKey: ['books'] })
  }
  const fail = (e: unknown) => setError((e as Error).message)

  const create = useMutation({
    mutationFn: (name: string) => shelvesApi.create(name, false),
    onSuccess: () => { setNewName(''); setError(''); refresh() },
    onError: fail,
  })
  const patch = useMutation({
    mutationFn: (v: { id: number; patch: Partial<Shelf> }) => shelvesApi.update(v.id, v.patch),
    onSuccess: refresh,
    onError: fail,
  })
  const remove = useMutation({
    mutationFn: (id: number) => shelvesApi.remove(id),
    onSuccess: refresh,
    onError: fail,
  })

  if (isLoading) return <p>Loading…</p>

  const mine = data?.shelves.filter((s) => s.mine) ?? []
  const others = data?.shelves.filter((s) => !s.mine) ?? []

  return (
    <div>
      {error && <div className="error">{error}</div>}

      <form
        style={{ display: 'flex', gap: 8, marginBottom: 18 }}
        onSubmit={(e) => {
          e.preventDefault()
          if (newName.trim()) create.mutate(newName.trim())
        }}
      >
        <input
          placeholder="New shelf name"
          value={newName}
          onChange={(e) => setNewName(e.target.value)}
          style={{
            flex: 1, height: 34, padding: '0 10px',
            border: '1px solid var(--border-strong)', borderRadius: 'var(--radius-sm)',
          }}
        />
        <button className="btn" type="submit" disabled={!newName.trim() || create.isPending}>
          Create shelf
        </button>
      </form>

      {mine.length === 0 ? (
        <p className="hint">
          You have no shelves yet. Create one above, then mark it
          <strong> Sync to Kobo</strong> to have its books appear on a device.
        </p>
      ) : (
      <table className="table">
        <thead>
          <tr>
            <th>Shelf</th>
            <th style={{ width: 80 }}>Books</th>
            <th style={{ width: 130 }}>Sync to Kobo</th>
            <th style={{ width: 90 }}>Public</th>
            <th style={{ width: 150 }}></th>
          </tr>
        </thead>
        <tbody>
          {mine.map((s) => (
            <tr key={s.id}>
              <td>
                <button className="linklike" onClick={() => onBrowse(s.id)}>
                  {s.name}
                </button>
              </td>
              <td>{s.book_count.toLocaleString('sv-SE')}</td>
              <td>
                <label className="switch">
                  <input
                    type="checkbox"
                    checked={s.kobo_sync}
                    onChange={(e) =>
                      patch.mutate({ id: s.id, patch: { kobo_sync: e.target.checked } })
                    }
                  />
                  <span>{s.kobo_sync ? 'Syncing' : 'Off'}</span>
                </label>
              </td>
              <td>
                <label className="switch">
                  <input
                    type="checkbox"
                    checked={s.is_public}
                    onChange={(e) =>
                      patch.mutate({ id: s.id, patch: { is_public: e.target.checked } })
                    }
                  />
                  <span>{s.is_public ? 'Shared' : 'Private'}</span>
                </label>
              </td>
              <td style={{ textAlign: 'right' }}>
                <button
                  className="btn btn--sm btn--ghost btn--danger"
                  onClick={() => {
                    if (
                      confirm(
                        `Delete the shelf "${s.name}"? The ${s.book_count} books stay in the library.`,
                      )
                    ) {
                      remove.mutate(s.id)
                    }
                  }}
                >
                  Delete
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      )}

      {mine.some((s) => s.kobo_sync) && (
        <p className="hint">
          Books on a syncing shelf appear on the Kobo as a Collection. Changes reach the
          device the next time it syncs.
        </p>
      )}

      {others.length > 0 && (
        <>
          <h3 style={{ marginTop: 26 }}>Shared with you</h3>
          <table className="table">
            <tbody>
              {others.map((s) => (
                <tr key={s.id}>
                  <td>
                    <button className="linklike" onClick={() => onBrowse(s.id)}>
                      {s.name}
                    </button>
                  </td>
                  <td style={{ width: 80 }}>{s.book_count.toLocaleString('sv-SE')}</td>
                  <td style={{ color: 'var(--text-muted)' }}>{s.owner}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
    </div>
  )
}
