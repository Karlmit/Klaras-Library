import { useRef, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { booksApi, type UploadResult } from '../api'

/**
 * Drag-and-drop book upload.
 *
 * The file goes through the same ingest pipeline as the watch folder, so it
 * gets the same treatment: duplicates recognised by content hash, metadata read
 * from inside the file, cover extracted, filed into the managed tree, KEPUB
 * queued.
 */
export function Upload({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient()
  const inputRef = useRef<HTMLInputElement>(null)
  const [dragging, setDragging] = useState(false)
  const [results, setResults] = useState<UploadResult[]>([])
  const [error, setError] = useState('')

  const send = useMutation({
    mutationFn: (files: File[]) => booksApi.upload(files),
    onSuccess: (r) => {
      setResults((prev) => [...r.results, ...prev])
      setError('')
      void qc.invalidateQueries({ queryKey: ['books'] })
      void qc.invalidateQueries({ queryKey: ['facets'] })
    },
    onError: (e) => setError((e as Error).message),
  })

  const accept = (list: FileList | null) => {
    if (!list?.length) return
    send.mutate(Array.from(list))
  }

  return (
    <>
      <div className="drawer-backdrop" onClick={onClose} />
      <aside className="drawer" role="dialog" aria-modal="true" aria-label="Add books">
        <button className="drawer__close" onClick={onClose} aria-label="Close">×</button>
        <h2 style={{ marginTop: 0 }}>Add books</h2>

        {error && <div className="error">{error}</div>}

        <div
          className={`dropzone ${dragging ? 'dropzone--over' : ''}`}
          onDragOver={(e) => { e.preventDefault(); setDragging(true) }}
          onDragLeave={() => setDragging(false)}
          onDrop={(e) => {
            e.preventDefault()
            setDragging(false)
            accept(e.dataTransfer.files)
          }}
          onClick={() => inputRef.current?.click()}
          role="button"
          tabIndex={0}
          onKeyDown={(e) => { if (e.key === 'Enter') inputRef.current?.click() }}
        >
          <input
            ref={inputRef}
            type="file"
            multiple
            accept=".epub,.kepub,.pdf,.mobi,.azw3,.cbz"
            hidden
            onChange={(e) => accept(e.target.files)}
          />
          {send.isPending ? (
            <strong>Uploading…</strong>
          ) : (
            <>
              <strong>Drop files here, or click to choose</strong>
              <p className="hint">EPUB, KEPUB, PDF, MOBI, AZW3 or CBZ</p>
            </>
          )}
        </div>

        <p className="hint">
          Metadata is read from inside the file. Anything already in the library
          is recognised by content and skipped, whatever the file is called.
        </p>

        {results.length > 0 && (
          <table className="table" style={{ marginTop: 16 }}>
            <tbody>
              {results.map((r, i) => (
                <tr key={i}>
                  <td>
                    {r.filename}
                    {r.error && <div className="warn">{r.error}</div>}
                  </td>
                  <td style={{ width: 110, textAlign: 'right' }}>
                    <span className={`pill pill--${r.status}`}>{r.status}</span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </aside>
    </>
  )
}
