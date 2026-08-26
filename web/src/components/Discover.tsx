import { useCallback, useEffect, useRef, useState } from 'react'
import { coverUrl, discoverApi, type DiscoverCard, type DiscoverStats } from '../api'

type Verdict = 'keep' | 'pass'

/**
 * Random book: one cover at a time, kept or passed with a swipe.
 *
 * A deck of candidates is held rather than one card, so the next cover is
 * already decoded when the current one leaves the screen — a swipe that waits
 * on a request feels broken however fast the request is. Decisions are sent in
 * the background and the card moves immediately; the only thing a reader can
 * lose to a failed request is one verdict, and Undo covers that.
 */
export function Discover({ onOpenShelf }: { onOpenShelf: (id: number) => void }) {
  const [deck, setDeck] = useState<DiscoverCard[]>([])
  const [stats, setStats] = useState<DiscoverStats | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [leaving, setLeaving] = useState<Verdict | null>(null)
  const [drag, setDrag] = useState({ x: 0, y: 0, active: false })
  const [last, setLast] = useState<{ card: DiscoverCard; verdict: Verdict } | null>(null)

  const card = deck[0]
  const next = deck[1]
  const busy = useRef(false)

  const refill = useCallback(async (replace: boolean) => {
    try {
      const r = await discoverApi.deck(8)
      setStats(r.stats)
      setDeck((d) => (replace ? r.cards : [...d, ...r.cards]))
      setError('')
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { void refill(true) }, [refill])

  const decide = useCallback(
    (verdict: Verdict) => {
      if (!card || busy.current) return
      busy.current = true
      setLeaving(verdict)
      const decided = card
      // Animate out first; the verdict travels while the next card settles.
      setTimeout(() => {
        setDeck((d) => d.slice(1))
        setLeaving(null)
        setDrag({ x: 0, y: 0, active: false })
        setLast({ card: decided, verdict })
        busy.current = false
      }, 260)
      discoverApi
        .decide(decided.id, verdict)
        .then((r) => setStats(r.stats))
        .catch((e) => setError((e as Error).message))
    },
    [card],
  )

  const undo = useCallback(async () => {
    if (!last) return
    try {
      const r = await discoverApi.decide(last.card.id, 'undo')
      setStats(r.stats)
      setDeck((d) => [last.card, ...d])
      setLast(null)
    } catch (e) {
      setError((e as Error).message)
    }
  }, [last])

  // Top up before the deck runs dry, so a swipe never waits on the network.
  useEffect(() => {
    if (!loading && deck.length > 0 && deck.length <= 2) void refill(false)
  }, [deck.length, loading, refill])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'ArrowLeft') decide('pass')
      if (e.key === 'ArrowRight') decide('keep')
      if (e.key === 'z' && (e.metaKey || e.ctrlKey)) void undo()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [decide, undo])

  // --- dragging ----------------------------------------------------------
  const start = useRef({ x: 0, y: 0 })
  const onDown = (e: React.PointerEvent) => {
    if (leaving) return
    // Capture keeps the drag tracking when the pointer leaves the card, which
    // is a nicety, not a requirement -- and it throws outright if the pointer
    // is already gone. Optional chaining does not help: the method exists, the
    // call is what fails. Letting that escape aborts the handler and the card
    // never becomes draggable at all.
    try {
      ;(e.currentTarget as HTMLElement).setPointerCapture?.(e.pointerId)
    } catch {
      // Drag still works; it just stops tracking if the pointer leaves.
    }
    start.current = { x: e.clientX, y: e.clientY }
    setDrag({ x: 0, y: 0, active: true })
  }
  const onMove = (e: React.PointerEvent) => {
    if (!drag.active) return
    setDrag({ x: e.clientX - start.current.x, y: e.clientY - start.current.y, active: true })
  }
  const onUp = () => {
    if (!drag.active) return
    const threshold = Math.min(120, window.innerWidth * 0.25)
    if (drag.x > threshold) decide('keep')
    else if (drag.x < -threshold) decide('pass')
    else setDrag({ x: 0, y: 0, active: false })
  }

  const offset = leaving ? (leaving === 'keep' ? window.innerWidth : -window.innerWidth) : drag.x
  const tilt = offset / 18
  const intent: Verdict | null =
    leaving ?? (drag.x > 60 ? 'keep' : drag.x < -60 ? 'pass' : null)

  if (loading) return <div className="disc"><p className="disc__msg">Shuffling…</p></div>

  return (
    <div className="disc">
      <header className="disc__bar">
        <div className="disc__tally">
          <span><b>{stats?.kept ?? 0}</b> kept</span>
          <span><b>{stats?.passed ?? 0}</b> passed</span>
          <span className="disc__left">{(stats?.remaining ?? 0).toLocaleString('sv-SE')} to go</span>
        </div>
        {stats && (
          <button className="btn btn--sm btn--ghost" onClick={() => onOpenShelf(stats.shelf_id)}>
            Open “{stats.shelf_name}”
          </button>
        )}
      </header>

      {error && <div className="error">{error}</div>}

      <div className="disc__stage">
        {!card ? (
          <div className="disc__done">
            <h2>That is the whole library.</h2>
            <p>You have looked at every book with a cover. Passed on something by mistake?
               Undo works for the last one; otherwise the shelf is waiting.</p>
            {stats && (
              <button className="btn" onClick={() => onOpenShelf(stats.shelf_id)}>
                Open “{stats.shelf_name}”
              </button>
            )}
          </div>
        ) : (
          <>
            {next && <Card key={next.id} card={next} behind />}
            <Card
              key={card.id}
              card={card}
              intent={intent}
              style={{
                transform: `translate(${offset}px, ${drag.y * 0.25}px) rotate(${tilt}deg)`,
                transition: drag.active ? 'none' : 'transform 260ms cubic-bezier(.2,.7,.3,1)',
              }}
              onPointerDown={onDown}
              onPointerMove={onMove}
              onPointerUp={onUp}
              onPointerCancel={onUp}
            />
          </>
        )}
      </div>

      {card && (
        <div className="disc__acts">
          <button className="disc__act disc__act--pass" onClick={() => decide('pass')}
                  aria-label="Not for me">
            <span className="disc__glyph" aria-hidden="true">✕</span>
            <span className="disc__label">Not for me</span>
          </button>
          <button className="disc__act disc__act--undo" onClick={() => void undo()}
                  disabled={!last} aria-label="Undo the last decision">
            <span className="disc__glyph" aria-hidden="true">↺</span>
            <span className="disc__label">Undo</span>
          </button>
          <button className="disc__act disc__act--keep" onClick={() => decide('keep')}
                  aria-label="Keep this one">
            <span className="disc__glyph" aria-hidden="true">♥</span>
            <span className="disc__label">Keep it</span>
          </button>
        </div>
      )}
    </div>
  )
}

function Card({
  card, behind, intent, style, ...handlers
}: {
  card: DiscoverCard
  behind?: boolean
  intent?: Verdict | null
  style?: React.CSSProperties
} & React.HTMLAttributes<HTMLElement>) {
  const meta: [string, string][] = []
  if (card.series) meta.push(['Series', card.series + (card.series_index != null ? ` #${card.series_index}` : '')])
  if (card.publisher) meta.push(['Publisher', card.publisher])
  if (card.pub_year) meta.push(['Published', String(card.pub_year)])
  if (card.languages?.length) meta.push(['Language', card.languages.join(', ')])
  if (card.formats?.length) meta.push(['Formats', card.formats.join(' · ')])
  if (card.rating) meta.push(['Rating', '★'.repeat(Math.round(card.rating / 2))])

  return (
    <article className={`disc__card ${behind ? 'disc__card--behind' : ''}`} style={style} {...handlers}>
      {intent && <span className={`disc__stamp disc__stamp--${intent}`}>
        {intent === 'keep' ? 'Keep' : 'Pass'}</span>}
      <div className="disc__cover">
        {card.has_cover
          ? <img src={coverUrl(card.id, 'detail')} alt="" draggable={false} />
          : <div className="disc__nocover" />}
      </div>
      <div className="disc__body">
        <h2 className="disc__title">{card.title}</h2>
        <p className="disc__by">{card.authors.join(', ') || 'Unknown'}</p>
        {card.tags?.length > 0 && (
          <div className="disc__tags">
            {card.tags.slice(0, 5).map((t) => <span key={t} className="disc__tag">{t}</span>)}
          </div>
        )}
        {card.description && <p className="disc__desc">{card.description}</p>}
        {meta.length > 0 && (
          <dl className="disc__meta">
            {meta.map(([k, v]) => (<div key={k}><dt>{k}</dt><dd>{v}</dd></div>))}
          </dl>
        )}
      </div>
    </article>
  )
}
