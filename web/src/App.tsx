import { Suspense, lazy, useCallback, useEffect, useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, type BookQuery, type User } from './api'
import { AuthScreen, ForcePasswordChange } from './components/Auth'
import { BulkBar } from './components/BulkBar'
import { EditPanel } from './components/EditPanel'
// epub.js is ~370kB and most visits never open a book in the browser, so the
// reader is split into its own chunk and fetched only when it is used.
const Reader = lazy(() =>
  import('./components/Reader').then((m) => ({ default: m.Reader })),
)
import { BrandMark } from './components/Brand'
import { Sidebar } from './components/Sidebar'
import { BookGrid } from './components/BookGrid'
import { BookDetail } from './components/BookDetail'

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
  const [selected, setSelected] = useState<number | null>(null)
  const [editing, setEditing] = useState<number | null>(null)
  const [reading, setReading] = useState<{ id: number; title: string; format: string } | null>(null)
  const [picked, setPicked] = useState<Set<number>>(new Set())
  const [, setLastPicked] = useState<number | null>(null)
  const [count, setCount] = useState<number | undefined>()
  const [query, setQuery] = useState<BookQuery>({ sort: 'title' })
  const [searchInput, setSearchInput] = useState('')

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
  useEffect(() => {
    const t = setTimeout(() => {
      setQuery((q) => {
        const next = searchInput.trim()
        if ((q.q ?? '') === next) return q
        // A text query switches to relevance ordering; clearing it goes back
        // to the previous stable sort.
        return { ...q, q: next || undefined, sort: next ? 'relevance' : 'title' }
      })
    }, 220)
    return () => clearTimeout(t)
  }, [searchInput])

  const patchQuery = useCallback((patch: Partial<BookQuery>) => {
    setQuery((q) => ({ ...q, ...patch }))
    // A changed filter invalidates the selection: keeping ids the user can no
    // longer see would make the next bulk action a surprise.
    setPicked(new Set())
  }, [])

  const togglePick = useCallback((id: number, shiftKey: boolean) => {
    setPicked((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
    if (!shiftKey) setLastPicked(id)
  }, [])

  const clearPicked = useCallback(() => setPicked(new Set()), [])

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

  return (
    <div className="app">
      <div className="brand">
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

      <Sidebar query={query} onChange={patchQuery} />

      <main className="main">
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
          onSelect={setSelected}
          onCount={setCount}
          selected={picked}
          onToggleSelect={togglePick}
        />
        <BulkBar selected={picked} onClear={clearPicked} />
      </main>

      {selected != null && (
        <BookDetail
          bookId={selected}
          onClose={() => setSelected(null)}
          onFilter={(patch) => patchQuery(patch)}
          onEdit={(id) => {
            setSelected(null)
            setEditing(id)
          }}
          onRead={(id, title, format) => {
            setSelected(null)
            setReading({ id, title, format })
          }}
          canEdit={user.role !== 'reader'}
        />
      )}

      {editing != null && <EditPanel bookId={editing} onClose={() => setEditing(null)} />}

      {reading && (
        <Suspense fallback={<div className="reader"><div className="reader__status">Loading reader…</div></div>}>
          <Reader
            bookId={reading.id}
            title={reading.title}
            format={reading.format}
            onClose={() => setReading(null)}
          />
        </Suspense>
      )}
    </div>
  )
}
