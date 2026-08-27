import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { browseApi, coverUrl, type SeriesEntry } from '../api'

/**
 * The series in the library, each shown as the books it holds.
 *
 * A series is recognised by its covers rather than by its name, so the card is
 * a fanned stack of the first few in order. There are only 39 here -- this
 * library is almost entirely standalone titles -- so no virtualising is
 * needed, and the cards can afford to be generous.
 */
export function SeriesView({ onPick }: { onPick: (name: string) => void }) {
  const { data, isLoading } = useQuery({
    queryKey: ['series'],
    queryFn: browseApi.series,
    staleTime: 5 * 60_000,
  })
  const [q, setQ] = useState('')

  const series = useMemo(() => {
    const all = data?.series ?? []
    const needle = q.trim().toLowerCase()
    return needle ? all.filter((s) => s.name.toLowerCase().includes(needle)) : all
  }, [data, q])

  return (
    <div className="browse">
      <div className="browse__bar">
        {/* "series" is its own plural, so only the count changes. */}
        <h1>{isLoading ? 'Series' : `${series.length} series`}</h1>
        <input
          className="browse__find"
          placeholder="Find a series…"
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />
      </div>

      <div className="browse__scroll">
        <div className="sgrid">
          {series.map((s) => (
            <SeriesCard key={s.id} series={s} onPick={onPick} />
          ))}
        </div>
        {!isLoading && !series.length && (
          <p className="browse__empty">No series matches that.</p>
        )}
      </div>
    </div>
  )
}

function SeriesCard({
  series,
  onPick,
}: {
  series: SeriesEntry
  onPick: (name: string) => void
}) {
  const covers = series.covers.slice(0, 4)
  return (
    <button className="scard" onClick={() => onPick(series.name)} title={series.name}>
      <span className="scard__fan">
        {covers.length ? (
          covers.map((c, i) => (
            <img
              key={c.id}
              src={coverUrl(c.id, 'grid', c.cover_v)}
              alt=""
              loading="lazy"
              // Later covers sit further right and behind, so the first book in
              // the series is the one fully visible.
              style={{ left: `${i * 22}%`, zIndex: covers.length - i }}
            />
          ))
        ) : (
          <span className="scard__nocovers">no covers</span>
        )}
        <span className="scard__count">{series.books}</span>
      </span>
      <span className="scard__name">{series.name}</span>
    </button>
  )
}
