import { useEffect, useMemo, useRef, useState } from 'react'
import { useInfiniteQuery } from '@tanstack/react-query'
import { useVirtualizer } from '@tanstack/react-virtual'
import { api, coverUrl, type BookListItem, type BookQuery } from '../api'

interface Props {
  query: BookQuery
  onSelect: (id: number) => void
  onCount: (n: number | undefined) => void
  selected: Set<number>
  onToggleSelect: (id: number, shiftKey: boolean) => void
}

const ROW_GAP = 20
const CARD_META_HEIGHT = 52

/**
 * Virtualised cover grid.
 *
 * Only the visible rows exist in the DOM, so 28,000 books cost the same as 60.
 * Rendering them all is what makes calibre-web's grid unusable at this size —
 * the browser, not the server, is the bottleneck once the query is fast.
 */
export function BookGrid({ query, onSelect, onCount, selected, onToggleSelect }: Props) {
  const scrollRef = useRef<HTMLDivElement>(null)

  const { data, fetchNextPage, hasNextPage, isFetchingNextPage, isLoading, error } =
    useInfiniteQuery({
      queryKey: ['books', query],
      initialPageParam: undefined as string | undefined,
      queryFn: ({ pageParam }) => api.books({ ...query, cursor: pageParam, total: true }),
      getNextPageParam: (last) => last.next_cursor,
    })

  const books: BookListItem[] = useMemo(
    () => data?.pages.flatMap((p) => p.items) ?? [],
    [data],
  )
  const total = data?.pages[0]?.total

  useEffect(() => {
    onCount(total)
  }, [total, onCount])

  // Columns are driven by CSS (auto-fill/minmax) so the virtualiser has to
  // measure the real grid rather than assume a count.
  const gridRef = useRef<HTMLDivElement>(null)
  const { columns, cardWidth } = useGridMetrics(gridRef)
  const rowHeight = Math.round(cardWidth * 1.5) + CARD_META_HEIGHT + ROW_GAP

  const rowCount = Math.ceil(books.length / Math.max(columns, 1))

  const virt = useVirtualizer({
    count: rowCount,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => rowHeight,
    overscan: 3,
  })

  // Load the next page when the last row comes into view.
  const items = virt.getVirtualItems()
  useEffect(() => {
    const last = items[items.length - 1]
    if (!last) return
    if (last.index >= rowCount - 2 && hasNextPage && !isFetchingNextPage) {
      void fetchNextPage()
    }
  }, [items, rowCount, hasNextPage, isFetchingNextPage, fetchNextPage])

  if (error) {
    return (
      <div className="empty">
        <strong>Could not load books</strong>
        <span>{(error as Error).message}</span>
      </div>
    )
  }

  if (!isLoading && books.length === 0) {
    return (
      <div className="empty">
        <strong>No books match</strong>
        <span>Try removing a filter or searching for something else.</span>
      </div>
    )
  }

  return (
    <div className="grid-scroll" ref={scrollRef}>
      {/* A zero-height probe that inherits the real grid template, so column
          count and card width are measured rather than guessed. */}
      <div className="grid" ref={gridRef} style={{ height: 0, overflow: 'hidden' }}>
        <div />
      </div>

      {isLoading ? (
        <div className="grid">
          {Array.from({ length: 24 }, (_, i) => (
            <div key={i}>
              <div className="card__cover skeleton" />
              <div className="skeleton" style={{ height: 12, marginTop: 8, borderRadius: 3 }} />
              <div
                className="skeleton"
                style={{ height: 10, marginTop: 6, width: '60%', borderRadius: 3 }}
              />
            </div>
          ))}
        </div>
      ) : (
        <div style={{ height: virt.getTotalSize(), position: 'relative' }}>
          {items.map((row) => {
            const start = row.index * columns
            const slice = books.slice(start, start + columns)
            return (
              <div
                key={row.key}
                className="grid"
                style={{
                  position: 'absolute',
                  top: 0,
                  left: 0,
                  width: '100%',
                  transform: `translateY(${row.start}px)`,
                }}
              >
                {slice.map((b) => (
                  <BookCard
                    key={b.id}
                    book={b}
                    onSelect={onSelect}
                    isSelected={selected.has(b.id)}
                    onToggleSelect={onToggleSelect}
                  />
                ))}
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}

function BookCard({
  book,
  onSelect,
  isSelected,
  onToggleSelect,
}: {
  book: BookListItem
  onSelect: (id: number) => void
  isSelected: boolean
  onToggleSelect: (id: number, shiftKey: boolean) => void
}) {
  return (
    <div
      className={`card ${isSelected ? 'card--selected' : ''}`}
      role="button"
      tabIndex={0}
      onClick={(e) => {
        // Ctrl/Cmd or Shift click selects rather than opens, which is what
        // every file manager and photo library does.
        if (e.metaKey || e.ctrlKey || e.shiftKey) {
          e.preventDefault()
          onToggleSelect(book.id, e.shiftKey)
          return
        }
        onSelect(book.id)
      }}
      onKeyDown={(e) => {
        if (e.key === 'Enter') {
          e.preventDefault()
          onSelect(book.id)
        }
        if (e.key === ' ') {
          e.preventDefault()
          onToggleSelect(book.id, false)
        }
      }}
    >
      <div className="card__cover">
        <button
          className="card__check"
          aria-label={isSelected ? 'Deselect' : 'Select'}
          aria-pressed={isSelected}
          onClick={(e) => {
            e.stopPropagation()
            onToggleSelect(book.id, e.shiftKey)
          }}
        >
          {isSelected ? '✓' : ''}
        </button>
        <img
          src={coverUrl(book.id, 'grid')}
          alt=""
          loading="lazy"
          decoding="async"
          width={200}
          height={300}
        />
        {book.needs_review && <span className="card__flag">review</span>}
      </div>
      <div className="card__title" title={book.title}>
        {book.title}
      </div>
      {book.series && (
        <div className="card__series">
          {book.series}
          {book.series_index != null && ` #${formatIndex(book.series_index)}`}
        </div>
      )}
      <div className="card__author" title={book.authors.join(', ')}>
        {book.authors.join(', ') || 'Unknown author'}
      </div>
    </div>
  )
}

function formatIndex(n: number): string {
  return Number.isInteger(n) ? String(n) : n.toFixed(1)
}

/**
 * Measures the grid's real column count and card width.
 *
 * Columns come from CSS (`repeat(auto-fill, minmax(...))`) so that the layout
 * stays responsive without JS breakpoints — which means the virtualiser has to
 * read the resulting geometry rather than assume it.
 */
function useGridMetrics(ref: React.RefObject<HTMLDivElement | null>) {
  const [metrics, setMetrics] = useState({ columns: 6, cardWidth: 170 })

  useEffect(() => {
    const el = ref.current
    if (!el) return

    const measure = () => {
      const columns = getComputedStyle(el)
        .gridTemplateColumns.split(' ')
        .filter(Boolean).length
      const width = el.clientWidth
      if (!columns || !width) return
      const cardWidth = Math.floor((width - 16 * (columns - 1)) / columns)
      setMetrics((prev) =>
        prev.columns === columns && Math.abs(prev.cardWidth - cardWidth) <= 1
          ? prev // same geometry: skip the render
          : { columns, cardWidth },
      )
    }

    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [ref])

  return metrics
}
