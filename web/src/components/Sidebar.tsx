import { useQuery } from '@tanstack/react-query'
import { api, shelvesApi, type BookQuery } from '../api'

interface Props {
  query: BookQuery
  onChange: (patch: Partial<BookQuery>) => void
  isAdmin: boolean
  /** Drawer state. Only has an effect below the layout breakpoint, where the
   *  sidebar is an overlay rather than a column. */
  open?: boolean
  /** Shown at the foot of the drawer on narrow screens, where the top bar has
   *  no room for these and the search field needs the whole width. */
  account?: { username: string; onSettings: () => void; onSignOut: () => void }
}

/**
 * Facet navigation.
 *
 * Counts come from a materialised view refreshed in the background: computed
 * live they cost ~22ms each, and there are five of them, so a page load would
 * spend most of its budget counting things that barely change.
 */
export function Sidebar({ query, onChange, isAdmin, open = false, account }: Props) {
  const { data } = useQuery({ queryKey: ['facets'], queryFn: api.facets, staleTime: 60_000 })
  const { data: shelves } = useQuery({ queryKey: ['shelves'], queryFn: shelvesApi.list })

  const clearAll = () =>
    onChange({
      author: undefined,
      tag: undefined,
      series: undefined,
      language: undefined,
      format: undefined,
      needs_review: false,
      shelf: undefined,
      adult: undefined,
    })

  const noFilter =
    !query.author && !query.tag && !query.series && !query.language && !query.format &&
    !query.needs_review && !query.shelf && !query.adult

  return (
    <nav className={`sidebar ${open ? 'sidebar--open' : ''}`} aria-label="Library filters">
      <div className="navhead">Library</div>
      <button
        className={`navitem ${noFilter ? 'navitem--active' : ''}`}
        onClick={clearAll}
      >
        <span className="navitem__label">All books</span>
        <span className="navitem__count">{data?.total_books?.toLocaleString('sv-SE') ?? ''}</span>
      </button>
      {isAdmin && !!data?.adult && (
        <button
          className={`navitem ${query.adult === 'only' ? 'navitem--active' : ''}`}
          onClick={() =>
            onChange(
              query.adult === 'only'
                ? { adult: undefined }
                : {
                    adult: 'only',
                    // The flagged set is its own view, not a filter on the
                    // current one: arriving here with an author still selected
                    // would show an empty page and look like nothing was found.
                    author: undefined, tag: undefined, series: undefined,
                    language: undefined, format: undefined, shelf: undefined,
                    needs_review: false, q: undefined,
                  },
            )
          }
          title="Erotica, hidden from every account except administrators. Clear the flag on anything caught by mistake, or delete the rest."
        >
          <span className="navitem__label">Adult content</span>
          <span className="navitem__count">{data.adult.toLocaleString('sv-SE')}</span>
        </button>
      )}
      {!!data?.needs_review && (
        <button
          className={`navitem ${query.needs_review ? 'navitem--active' : ''}`}
          onClick={() => onChange({ needs_review: !query.needs_review })}
          title="Books whose imported metadata looked suspect"
        >
          <span className="navitem__label">Needs review</span>
          <span className="navitem__count">{data.needs_review.toLocaleString('sv-SE')}</span>
        </button>
      )}

      {!!shelves?.shelves.length && (
        <>
          <div className="navhead">Shelves</div>
          {shelves.shelves.map((sh) => (
            <button
              key={sh.id}
              className={`navitem ${query.shelf === sh.id ? 'navitem--active' : ''}`}
              onClick={() => onChange({ shelf: query.shelf === sh.id ? undefined : sh.id })}
              title={sh.kobo_sync ? 'Synced to Kobo' : sh.name}
            >
              <span className="navitem__label">
                {sh.kobo_sync && <span className="kobo-dot" aria-label="Kobo" />}
                {sh.name}
              </span>
              <span className="navitem__count">{sh.book_count.toLocaleString('sv-SE')}</span>
            </button>
          ))}
        </>
      )}

      <FacetGroup
        title="Authors"
        items={data?.authors}
        active={query.author}
        onPick={(v) => onChange({ author: v === query.author ? undefined : v })}
      />
      <FacetGroup
        title="Categories"
        items={data?.tags}
        active={query.tag}
        onPick={(v) => onChange({ tag: v === query.tag ? undefined : v })}
      />
      <FacetGroup
        title="Series"
        items={data?.series}
        active={query.series}
        onPick={(v) => onChange({ series: v === query.series ? undefined : v })}
      />
      <FacetGroup
        title="Languages"
        items={data?.languages}
        active={query.language}
        onPick={(v) => onChange({ language: v === query.language ? undefined : v })}
      />
      <FacetGroup
        title="Formats"
        items={data?.formats}
        active={query.format}
        onPick={(v) => onChange({ format: v === query.format ? undefined : v })}
      />
      {account && (
        <div className="sidebar__account">
          <div className="navhead">{account.username}</div>
          <button className="navitem" onClick={account.onSettings}>
            <span className="navitem__label">Settings</span>
          </button>
          <button className="navitem" onClick={account.onSignOut}>
            <span className="navitem__label">Sign out</span>
          </button>
        </div>
      )}
    </nav>
  )
}

function FacetGroup({
  title,
  items,
  active,
  onPick,
  limit = 15,
}: {
  title: string
  items?: { value: string; count: number }[]
  active?: string
  onPick: (v: string) => void
  limit?: number
}) {
  if (!items?.length) return null
  // Keep the active value visible even if it falls outside the top slice.
  const shown = items.slice(0, limit)
  if (active && !shown.some((i) => i.value === active)) {
    const found = items.find((i) => i.value === active)
    if (found) shown.unshift(found)
  }
  return (
    <>
      <div className="navhead">{title}</div>
      {shown.map((f) => (
        <button
          key={f.value}
          className={`navitem ${active === f.value ? 'navitem--active' : ''}`}
          onClick={() => onPick(f.value)}
          title={f.value}
        >
          <span className="navitem__label">{f.value}</span>
          <span className="navitem__count">{f.count.toLocaleString('sv-SE')}</span>
        </button>
      ))}
    </>
  )
}
