import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { descriptionsApi, type DescriptionStatus } from '../api'
import { useState } from 'react'

/**
 * What the nightly description backfill has done, and what is left.
 *
 * It exists because a job that runs itself is a job nobody can see. The two
 * questions worth answering on a screen are "did it run last night" and "when
 * is it finished", and neither is answerable from a container log by the person
 * who wants to know.
 */
export function SettingsDescriptions() {
  const qc = useQueryClient()
  const [error, setError] = useState('')
  const { data, isLoading } = useQuery({
    queryKey: ['descriptions'],
    queryFn: descriptionsApi.status,
    // Poll while a run is going so the numbers move rather than going stale.
    refetchInterval: (q) => ((q.state.data as DescriptionStatus | undefined)?.running ? 3000 : false),
  })

  const run = useMutation({
    mutationFn: descriptionsApi.run,
    onSuccess: () => { setError(''); void qc.invalidateQueries({ queryKey: ['descriptions'] }) },
    onError: (e) => setError((e as Error).message),
  })

  if (isLoading || !data) return <p>Loading…</p>

  const pct = data.total ? Math.round((100 * data.with_description) / data.total) : 0
  const peak = Math.max(1, ...data.recent.map((d) => d.asked))
  const daysLeft = data.remaining > 0 ? Math.ceil(data.remaining / 900) : 0
  const lastRun = data.last_run ? new Date(data.last_run) : null
  const staleHours = lastRun ? (Date.now() - lastRun.getTime()) / 3.6e6 : Infinity

  return (
    <div>
      {error && <div className="error">{error}</div>}

      <p className="hint" style={{ marginTop: 0 }}>
        Books arrive from Calibre with no description about half the time. Every night the
        server reads what it can from the books’ own files, then asks Google Books about
        anything with an ISBN, up to the daily free allowance.
      </p>

      <div className="desc__bar" role="img"
           aria-label={`${data.with_description} of ${data.total} books have a description`}>
        <div className="desc__fill" style={{ width: `${pct}%` }} />
      </div>
      <p className="desc__legend">
        <strong>{data.with_description.toLocaleString('sv-SE')}</strong> of{' '}
        {data.total.toLocaleString('sv-SE')} books have a description
        <span className="desc__pct">{pct}%</span>
      </p>

      <dl className="desc__stats">
        <div><dt>Still to try</dt><dd>{data.remaining.toLocaleString('sv-SE')}</dd></div>
        <div><dt>Out of reach</dt><dd>{data.unreachable.toLocaleString('sv-SE')}</dd></div>
        <div><dt>Found in the books</dt><dd>{data.found_in_files.toLocaleString('sv-SE')}</dd></div>
        <div><dt>Found via Google</dt><dd>{data.found_via_google.toLocaleString('sv-SE')}</dd></div>
      </dl>

      <p className="hint">
        {data.remaining > 0
          ? <>At the free allowance of 900 lookups a day, about{' '}
              <strong>{daysLeft} more {daysLeft === 1 ? 'night' : 'nights'}</strong> to work through
              the rest.</>
          : <>Nothing left to try. The {data.unreachable.toLocaleString('sv-SE')} without a
              description have no ISBN, or Google had no record of them.</>}
      </p>

      {!data.google_enabled && (
        <p className="warn">
          No Google Books key is set, so only the books’ own files are read — roughly one book
          in eleven. Add <code>KLARAS_GOOGLE_BOOKS_KEY</code> to the container to use Google.
        </p>
      )}

      <h3 className="desc__h">Last two weeks</h3>
      <div className="desc__spark">
        {data.recent.map((d) => (
          <div key={d.day} className="desc__day"
               title={`${d.day}: asked about ${d.asked}, found ${d.found}`}>
            <div className="desc__col">
              <div className="desc__asked" style={{ height: `${(100 * d.asked) / peak}%` }} />
              <div className="desc__found" style={{ height: `${(100 * d.found) / peak}%` }} />
            </div>
            <span className="desc__tick">{d.day.slice(8)}</span>
          </div>
        ))}
      </div>
      <p className="desc__key">
        <span className="desc__swatch desc__swatch--found" /> found
        <span className="desc__swatch desc__swatch--asked" /> asked
      </p>

      <p className="hint">
        {data.running
          ? 'Running now — the numbers above refresh as it goes.'
          : lastRun
            ? `Last ran ${lastRun.toLocaleString('sv-SE')}${staleHours > 36 ? ' — longer ago than a day, which is worth a look' : ''}.`
            : 'Has not run yet. It starts a few minutes after the server does.'}
      </p>

      <button className="btn" disabled={data.running || run.isPending}
              onClick={() => run.mutate()}>
        {data.running ? 'Running…' : 'Run now'}
      </button>
    </div>
  )
}
