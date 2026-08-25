import { useEffect, useRef, useState } from 'react'
import ePub, { type Rendition } from 'epubjs'
import { downloadUrl } from '../api'

interface Props {
  bookId: number
  title: string
  format: string
  onClose: () => void
}

/**
 * In-browser EPUB reader.
 *
 * Reading position is kept in localStorage rather than on the server: this is a
 * casual "read a few pages on a laptop" reader, and the authoritative progress
 * for a book lives on the Kobo, which syncs its own state. Writing browser
 * progress back would fight with the device.
 */
export function Reader({ bookId, title, format, onClose }: Props) {
  const hostRef = useRef<HTMLDivElement>(null)
  const renditionRef = useRef<Rendition | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const storageKey = `klaras:reader:${bookId}`

  useEffect(() => {
    const host = hostRef.current
    if (!host) return

    let cancelled = false
    const book = ePub(downloadUrl(bookId, format))
    const rendition = book.renderTo(host, {
      width: '100%',
      height: '100%',
      spread: 'auto',
    })
    renditionRef.current = rendition

    let saved: string | null = null
    try {
      saved = localStorage.getItem(storageKey)
    } catch {
      // Private browsing, or storage disabled. Start from the beginning.
    }

    rendition
      .display(saved ?? undefined)
      .then(() => {
        if (!cancelled) setLoading(false)
      })
      .catch((e: unknown) => {
        if (!cancelled) {
          setError(e instanceof Error ? e.message : 'Could not open this book')
          setLoading(false)
        }
      })

    rendition.on('relocated', (location: { start: { cfi: string } }) => {
      try {
        localStorage.setItem(storageKey, location.start.cfi)
      } catch {
        // Nothing to do; the reader still works, it just will not resume.
      }
    })

    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'ArrowRight') void rendition.next()
      if (e.key === 'ArrowLeft') void rendition.prev()
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    // epub.js renders into an iframe, which swallows key events of its own.
    rendition.on('keyup', onKey)

    return () => {
      cancelled = true
      window.removeEventListener('keydown', onKey)
      rendition.destroy()
      book.destroy()
    }
  }, [bookId, format, onClose, storageKey])

  return (
    <div className="reader">
      <div className="reader__bar">
        <button className="btn btn--sm btn--ghost" onClick={onClose}>
          ← Close
        </button>
        <span className="reader__title">{title}</span>
        <div style={{ flex: 1 }} />
        <button className="btn btn--sm btn--ghost" onClick={() => renditionRef.current?.prev()}>
          ‹ Previous
        </button>
        <button className="btn btn--sm btn--ghost" onClick={() => renditionRef.current?.next()}>
          Next ›
        </button>
      </div>
      {loading && <div className="reader__status">Opening…</div>}
      {error && (
        <div className="reader__status">
          <strong>Could not open this book</strong>
          <div style={{ color: 'var(--text-muted)', marginTop: 4 }}>{error}</div>
        </div>
      )}
      <div className="reader__page" ref={hostRef} />
    </div>
  )
}
