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

    // Declared up front: the async body below registers it on the rendition,
    // and the cleanup removes it.
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'ArrowRight') void renditionRef.current?.next()
      if (e.key === 'ArrowLeft') void renditionRef.current?.prev()
      if (e.key === 'Escape') onClose()
    }
    // epub.js renders into an iframe, which swallows key events of its own,
    // so the rendition gets its own listener as well as the window.
    window.addEventListener('keydown', onKey)

    let cancelled = false
    let book: ReturnType<typeof ePub> | null = null
    let rendition: Rendition | null = null

    // Fetch the archive ourselves and hand epub.js the bytes.
    //
    // Passing the URL instead makes epub.js guess how to open it from the file
    // extension, and our download route ends in "/epub" rather than ".epub".
    // It concluded the book was an unpacked directory and started requesting
    // "download/META-INF/container.xml", which 404s and leaves the reader
    // stuck on "Opening...".
    ;(async () => {
      try {
        const res = await fetch(downloadUrl(bookId, format))
        if (!res.ok) {
          throw new Error(
            res.status === 404
              ? 'This book has no EPUB file on disk.'
              : `The server returned ${res.status}.`,
          )
        }
        const buf = await res.arrayBuffer()
        if (cancelled) return

        book = ePub(buf)
        rendition = book.renderTo(host, { width: '100%', height: '100%', spread: 'auto' })
        renditionRef.current = rendition

        let saved: string | null = null
        try {
          saved = localStorage.getItem(storageKey)
        } catch {
          // Private browsing, or storage disabled. Start from the beginning.
        }

        rendition.on('relocated', (location: { start: { cfi: string } }) => {
          try {
            localStorage.setItem(storageKey, location.start.cfi)
          } catch {
            // The reader still works; it just will not resume next time.
          }
        })
        rendition.on('keyup', onKey)

        await rendition.display(saved ?? undefined)
        if (!cancelled) setLoading(false)
      } catch (e) {
        if (!cancelled) {
          setError(e instanceof Error ? e.message : 'Could not open this book')
          setLoading(false)
        }
      }
    })()

    return () => {
      cancelled = true
      window.removeEventListener('keydown', onKey)
      rendition?.destroy()
      book?.destroy()
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
