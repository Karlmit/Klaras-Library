import { Suspense, lazy, useCallback, useEffect, useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, selectionApi, shelvesApi, type BookQuery, type User } from './api'
import { AuthScreen, ForcePasswordChange } from './components/Auth'
import { BulkBar } from './components/BulkBar'
import { EditPanel } from './components/EditPanel'
// epub.js is ~370kB and most visits never open a book in the browser, so the
// reader is split into its own chunk and fetched only when it is used.
const Discover = lazy(() =>
  import('./components/Discover').then((m) => ({ default: m.Discover })),
)
const AuthorsView = lazy(() =>
  import('./components/AuthorsView').then((m) => ({ default: m.AuthorsView })),
)
const SeriesView = lazy(() =>
  import('./components/SeriesView').then((m) => ({ default: m.SeriesView })),
)
const CategoryMerge = lazy(() =>
  import('./components/CategoryMerge').then((m) => ({ default: m.CategoryMerge })),
)
const AuthorPage = lazy(() =>
  import('./components/AuthorPage').then((m) => ({ default: m.AuthorPage })),
)
const Reader = lazy(() =>
  import('./components/Reader').then((m) => ({ default: m.Reader })),
)
import { BrandMark } from './components/Brand'
import { Sidebar } from './components/Sidebar'
import { BookGrid } from './components/BookGrid'
import { BookDetail } from './components/BookDetail'
import { Settings } from './components/Settings'
import { Upload } from './components/Upload'
import { useLocation, navigate, goBack, href } from './router'

const SORTS: { value: string; label: string }[] = [
  { value: 'title', label: 'Title A–Ö' },
  { value: 'author', label: 'Author A–Ö' },
  { value: 'added', label: 'Recently added' },
  { value: 'pubdate', label: 'Publication date' },
  { value: 'rating', label: 'Rating' },
  { value: 'series', label: 'Series' },
]

export function App() {
  const qc = useQueryClient()
  const [user, setUser] = useState<User | null>(null)
  const [picked, setPicked] = useState<Set<number>>(new Set())
  const [lastPicked, setLastPicked] = useState<number | null>(null)
  const [count, setCount] = useState<number | undefined>()
  const [searchInput, setSearchInput] = useState('')

  // Every view is a place with an address, so Back and Forward are the
  // browser's own rather than an imitation of them, and a page can be
  // bookmarked, shared, or reloaded without landing back at the top.
  const { path, params } = useLocation()

  const query = useMemo<BookQuery>(() => {
    const num = (k: string) => (params.get(k) ? Number(params.get(k)) : undefined)
    return {
      q: params.get('q') ?? undefined,
      author: params.get('author') ?? undefined,
      tag: params.get('tag') ?? undefined,
      series: params.get('series') ?? undefined,
      language: params.get('language') ?? undefined,
      format: params.get('format') ?? undefined,
      shelf: num('shelf'),
      needs_review: params.get('needs_review') === '1',
      adult: (params.get('adult') as BookQuery['adult']) ?? undefined,
      sort: params.get('sort') ?? 'title',
    }
  }, [params])

  const libraryHref = useCallback(
    (patch: Partial<BookQuery>) => {
      const next = { ...query, ...patch }
      return href('/', {
        q: next.q, author: next.author, tag: next.tag, series: next.series,
        language: next.language, format: next.format, shelf: next.shelf,
        needs_review: next.needs_review ? '1' : undefined,
        adult: next.adult,
        sort: next.sort && next.sort !== 'title' ? next.sort : undefined,
      })
    },
    [query],
  )

  // Overlays and pages are read off the address rather than held in state.
  const m = {
    book: /^\/books\/(\d+)$/.exec(path),
    edit: /^\/books\/(\d+)\/edit$/.exec(path),
    read: /^\/read\/(\d+)$/.exec(path),
    author: /^\/authors\/(\d+)$/.exec(path),
  }
  const selected = m.book ? Number(m.book[1]) : m.edit ? Number(m.edit[1]) : null
  const editing = m.edit ? Number(m.edit[1]) : null
  const readingId = m.read ? Number(m.read[1]) : null
  const authorId = m.author ? Number(m.author[1]) : null
  const browse: 'authors' | 'series' | null =
    path === '/authors' ? 'authors' : path === '/series' ? 'series' : null
  const settingsOpen = path === '/settings'
  const uploadOpen = path === '/upload'
  const discoverOpen = path === '/discover'
  const tidyTags = path === '/categories'

  // The nav drawer is not a place -- it is a way of getting to one -- so it
  // stays component state and closes on any navigation.
  const [navOpen, setNavOpen] = useState(false)
  const closeNav = useCallback(() => setNavOpen(false), [])
  useEffect(() => { setNavOpen(false) }, [path])

  const closeDetail = useCallback(() => goBack('/'), [])
  const closeEdit = useCallback(() => goBack('/'), [])
  const closeReader = useCallback(() => goBack('/'), [])
  const closeSettings = useCallback(() => goBack('/'), [])
  const closeUpload = useCallback(() => goBack('/'), [])
  const closeDiscover = useCallback(() => goBack('/'), [])
  const closeBrowse = useCallback(() => goBack('/'), [])

  const { data: status, isLoading: statusLoading } = useQuery({
    queryKey: ['status'],
    queryFn: api.status,
  })
  const { data: me, isLoading: meLoading } = useQuery({
    queryKey: ['me'],
    queryFn: api.me,
  })

  useEffect(() => {
    if (me?.authenticated && me.user) setUser(me.user)
  }, [me])

  // Debounce the search box: typing "Läckberg" would otherwise fire nine
  // queries, and only the last one matters.
  // Debounced, and replacing rather than pushing: typing a title should not
  // bury the page you came from under one history entry per keystroke.
  useEffect(() => {
    const t = setTimeout(() => {
      const next = searchInput.trim()
      if ((query.q ?? '') === next) return
      navigate(
        libraryHref({ q: next || undefined, sort: next ? 'relevance' : 'title' }),
        { replace: path === '/' },
      )
    }, 220)
    return () => clearTimeout(t)
  }, [searchInput, query.q, libraryHref, path])

  // Coming back to a search through history should refill the box.
  useEffect(() => { setSearchInput(query.q ?? '') }, [query.q])

  const patchQuery = useCallback((patch: Partial<BookQuery>) => {
    navigate(libraryHref(patch))
    // A changed filter invalidates the selection: keeping ids the user can no
    // longer see would make the next bulk action a surprise.
    setPicked(new Set())
    setLastPicked(null)
  }, [libraryHref])

  const togglePick = useCallback(
    (id: number, shiftKey: boolean, visible: number[]) => {
      setPicked((prev) => {
        const next = new Set(prev)

        // Shift extends from the last plain click to here, following the order
        // currently on screen rather than book ids -- the range someone means
        // is the one they can see.
        if (shiftKey && lastPicked != null && lastPicked !== id) {
          const from = visible.indexOf(lastPicked)
          const to = visible.indexOf(id)
          if (from !== -1 && to !== -1) {
            const [lo, hi] = from < to ? [from, to] : [to, from]
            for (let i = lo; i <= hi; i++) next.add(visible[i]!)
            return next
          }
        }

        if (next.has(id)) next.delete(id)
        else next.add(id)
        return next
      })
      // A shift-click extends the existing anchor rather than moving it, so a
      // second shift-click can grow or shrink the same range.
      if (!shiftKey) setLastPicked(id)
    },
    [lastPicked],
  )

  const [selectingAll, setSelectingAll] = useState(false)

  // Selecting everything that matches the current filter, not just what has
  // been scrolled into view. Without this, fixing 6,706 books tagged "F" would
  // mean scrolling the whole way and shift-clicking.
  const selectAll = useCallback(async () => {
    setSelectingAll(true)
    try {
      const r = await selectionApi.ids(query)
      setPicked(new Set(r.ids))
      if (r.truncated) {
        alert(
          `This filter matches more than ${r.limit.toLocaleString('sv-SE')} books. ` +
            `The first ${r.count.toLocaleString('sv-SE')} are selected; ` +
            `narrow the filter to reach the rest.`,
        )
      }
    } catch (e) {
      alert(`Could not select everything: ${(e as Error).message}`)
    } finally {
      setSelectingAll(false)
    }
  }, [query])

  const clearPicked = useCallback(() => {
    setPicked(new Set())
    setLastPicked(null)
  }, [])

  // The shelf currently being viewed, if any. Only a shelf the reader owns
  // counts: the bulk bar offers removal, and that is not theirs to do on
  // someone else's public shelf.
  const { data: shelfList } = useQuery({ queryKey: ['shelves'], queryFn: shelvesApi.list })
  const activeShelf = useMemo(() => {
    if (!query.shelf) return null
    // An admin may prune anyone's shelf -- the server has always allowed it,
    // and requiring ownership here was the only reason the button never
    // appeared while looking at a shelf belonging to another account.
    const s = shelfList?.shelves.find(
      (x) => x.id === query.shelf && (x.mine || user?.role === 'admin'),
    )
    return s ? { id: s.id, name: s.name } : null
  }, [query.shelf, shelfList, user?.role])

  const activeChips = useMemo(() => {
    const chips: { key: keyof BookQuery; label: string; value: string }[] = []
    if (query.author) chips.push({ key: 'author', label: 'Author', value: query.author })
    if (query.tag) chips.push({ key: 'tag', label: 'Category', value: query.tag })
    if (query.series) chips.push({ key: 'series', label: 'Series', value: query.series })
    if (query.language) chips.push({ key: 'language', label: 'Language', value: query.language })
    if (query.format) chips.push({ key: 'format', label: 'Format', value: query.format })
    if (query.needs_review) chips.push({ key: 'needs_review', label: '', value: 'Needs review' })
    return chips
  }, [query])

  if (statusLoading || meLoading) {
    return <div className="auth"><div className="auth__card">Loading…</div></div>
  }
  if (status?.needs_setup && !user) {
    return <AuthScreen mode="setup" onDone={(u) => { setUser(u); void qc.invalidateQueries() }} />
  }
  if (!user) {
    return <AuthScreen mode="login" onDone={(u) => { setUser(u); void qc.invalidateQueries() }} />
  }
  if (user.password_reset_required) {
    return (
      <ForcePasswordChange
        onDone={() => {
          setUser({ ...user, password_reset_required: false })
          void qc.invalidateQueries()
        }}
      />
    )
  }

  if (settingsOpen) {
    return (
      <Settings
        user={user}
        onClose={closeSettings}
        onBrowseShelf={(id) => patchQuery({ shelf: id })}
      />
    )
  }

  return (
    <div className="app">
      <div className="brand">
        <button
          className="brand__menu"
          onClick={() => setNavOpen((v) => !v)}
          aria-label={navOpen ? 'Close navigation' : 'Open navigation'}
          aria-expanded={navOpen}
        >
          <span aria-hidden="true">{navOpen ? '✕' : '☰'}</span>
        </button>
        <BrandMark />
        <span className="brand__name">Klaras Library</span>
      </div>

      <header className="topbar">
        <div className="search">
          <span className="search__icon" aria-hidden="true">⌕</span>
          <label className="visually-hidden" htmlFor="q">Search the library</label>
          <input
            id="q"
            value={searchInput}
            onChange={(e) => setSearchInput(e.target.value)}
            placeholder="Search titles and authors…"
            autoComplete="off"
            type="search"
          />
          {searchInput && (
            <button className="search__clear" onClick={() => setSearchInput('')} aria-label="Clear search">
              ×
            </button>
          )}
        </div>
        <div className="topbar__spacer" />
        <div className="usermenu">
          <button
            className="btn btn--ghost btn--sm"
            onClick={() => navigate('/settings')}
            title="Kobo devices, shelves, users"
          >
            Settings
          </button>
          <span className="usermenu__name">{user.username}</span>
          <button
            className="btn btn--ghost btn--sm"
            onClick={async () => {
              await api.logout()
              setUser(null)
              void qc.invalidateQueries()
            }}
          >
            Sign out
          </button>
        </div>
      </header>

      {navOpen && <div className="scrim" onClick={closeNav} aria-hidden="true" />}
      <Sidebar
        query={query}
        onChange={(patch) => {
          patchQuery(patch)
          // On a phone the drawer covers the grid, so leaving it open after a
          // choice hides the result of that choice.
          closeNav()
        }}
        isAdmin={user.role === 'admin'}
        open={navOpen}
        onDiscover={() => navigate('/discover')}
        onAuthors={() => navigate('/authors')}
        onSeries={() => navigate('/series')}
        here={authorId != null ? 'authors' : browse}
        onCategories={user.role !== 'reader' ? () => navigate('/categories') : undefined}
        account={{
          username: user.username,
          onSettings: () => navigate('/settings'),
          onSignOut: async () => {
            await api.logout()
            setUser(null)
            void qc.invalidateQueries()
          },
        }}
      />

      <main className="main">
        {/* Authors, Series and one author's page live inside the layout rather
            than as full-screen sheets. Taking the navigation away to show a
            navigation page was backwards: the sidebar is how you get from
            Authors to Series or back to a shelf. */}
        {browse === 'authors' && (
          <Suspense fallback={<p className="browse__empty">Loading…</p>}>
            <AuthorsView onPick={(id) => navigate(`/authors/${id}`)} />
          </Suspense>
        )}

        {browse === 'series' && (
          <Suspense fallback={<p className="browse__empty">Loading…</p>}>
            <SeriesView
              onPick={(name) =>
                navigate(href('/', { series: name, sort: 'series' }))
              }
            />
          </Suspense>
        )}

        {authorId != null && (
          <Suspense fallback={<p className="browse__empty">Loading…</p>}>
            <div className="crumbs">
              <button className="linkish" onClick={() => navigate('/authors')}>Authors</button>
              <span aria-hidden="true">›</span>
              <span>this author</span>
            </div>
            <AuthorPage
              authorId={authorId}
              onBooks={(name) => navigate(href('/', { author: name }))}
            />
          </Suspense>
        )}

        {browse === null && authorId == null && (
        <>
        <div className="toolbar">
          <span className="toolbar__count">
            {count != null ? `${count.toLocaleString('sv-SE')} books` : ' '}
          </span>
          {activeChips.map((c) => (
            <span className="chip" key={String(c.key)}>
              {c.label && <em style={{ fontStyle: 'normal', opacity: 0.7 }}>{c.label}:</em>}
              {c.value}
              <button
                onClick={() =>
                  patchQuery({ [c.key]: c.key === 'needs_review' ? false : undefined } as Partial<BookQuery>)
                }
                aria-label={`Remove ${c.value} filter`}
              >
                ×
              </button>
            </span>
          ))}
          <div className="topbar__spacer" />
          <button
            className="btn btn--sm btn--ghost"
            onClick={() => void selectAll()}
            disabled={selectingAll}
            title="Select every book matching the current filter"
          >
            {selectingAll ? 'Selecting…' : 'Select all'}
          </button>
          {user.role !== 'reader' && (
            <button className="btn btn--sm" onClick={() => navigate('/upload')}>
              Add books
            </button>
          )}
          <label className="visually-hidden" htmlFor="sort">Sort by</label>
          <select
            id="sort"
            className="sort"
            value={query.sort ?? 'title'}
            onChange={(e) => patchQuery({ sort: e.target.value })}
          >
            {query.q && <option value="relevance">Best match</option>}
            {SORTS.map((s) => (
              <option key={s.value} value={s.value}>{s.label}</option>
            ))}
          </select>
        </div>

        <BookGrid
          query={query}
          onSelect={(id: number) => navigate(`/books/${id}`)}
          onCount={setCount}
          selected={picked}
          onToggleSelect={togglePick}
        />
        <BulkBar
          selected={picked}
          onClear={clearPicked}
          reviewingAdult={query.adult === 'only'}
          shelf={activeShelf}
        />
        </>
        )}
      </main>

      {selected != null && (
        <BookDetail
          bookId={selected}
          onClose={closeDetail}
          onFilter={(patch) => patchQuery(patch)}
          onEdit={(id) => navigate(`/books/${id}/edit`)}
          onRead={(id) => navigate(`/read/${id}`)}
          canEdit={user.role !== 'reader'}
        />
      )}

      {editing != null && <EditPanel bookId={editing} onClose={closeEdit} />}

      {uploadOpen && <Upload onClose={closeUpload} />}

      {tidyTags && (
        <Suspense fallback={null}>
          <CategoryMerge onClose={closeBrowse} />
        </Suspense>
      )}

      {discoverOpen && (
        <div className="sheet" role="dialog" aria-label="Random book">
          <div className="sheet__head">
            <button className="btn btn--sm btn--ghost" onClick={closeDiscover}>← Back to library</button>
            <h1 className="sheet__title">Random book</h1>
          </div>
          <Suspense fallback={<div className="disc"><p className="disc__msg">Shuffling…</p></div>}>
            <Discover
              onOpenShelf={(id) => {
                closeDiscover()
                patchQuery({ shelf: id, author: undefined, tag: undefined, series: undefined,
                             language: undefined, format: undefined, needs_review: false,
                             adult: undefined, q: undefined })
              }}
            />
          </Suspense>
        </div>
      )}

      {readingId != null && (
        <Suspense fallback={<div className="reader"><div className="reader__status">Loading reader…</div></div>}>
          {/* Title and format used to be handed over by whoever clicked Read.
              An address carries only the id, so the reader looks the book up
              itself -- which is also what makes /read/123 survive a reload. */}
          <ReaderRoute bookId={readingId} onClose={closeReader} />
        </Suspense>
      )}
    </div>
  )
}

/** Resolves a book id into what the reader needs. */
function ReaderRoute({ bookId, onClose }: { bookId: number; onClose: () => void }) {
  const { data: book, isLoading } = useQuery({
    queryKey: ['book', bookId],
    queryFn: () => api.book(bookId),
  })
  if (isLoading) {
    return <div className="reader"><div className="reader__status">Loading…</div></div>
  }
  const format = book?.files.find((f) => f.format === 'EPUB')?.format
  if (!book || !format) {
    return (
      <div className="reader">
        <div className="reader__status">
          This book has no EPUB to read.
          <button className="btn btn--sm btn--ghost" onClick={onClose} style={{ marginLeft: 10 }}>
            Close
          </button>
        </div>
      </div>
    )
  }
  return <Reader bookId={book.id} title={book.title} format={format} onClose={onClose} />
}
