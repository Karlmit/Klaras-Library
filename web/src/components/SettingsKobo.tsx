import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { koboApi } from '../api'

/**
 * Kobo device pairing.
 *
 * The token is a bearer credential that lives in the URL the device is
 * configured with, because that is the only thing a Kobo can be told. It is
 * shown in full once, on creation, since that is when it needs copying; after
 * that only its age and last use are listed.
 */
export function SettingsKobo() {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({ queryKey: ['kobo-tokens'], queryFn: koboApi.tokens })
  const [label, setLabel] = useState('')
  const [fresh, setFresh] = useState<{ token: string; api_store_url: string } | null>(null)
  const [copied, setCopied] = useState(false)
  const [error, setError] = useState('')
  const [resynced, setResynced] = useState<number | null>(null)

  const create = useMutation({
    mutationFn: (l: string) => koboApi.create(l),
    onSuccess: (r) => {
      setFresh(r)
      setLabel('')
      setError('')
      void qc.invalidateQueries({ queryKey: ['kobo-tokens'] })
    },
    onError: (e) => setError((e as Error).message),
  })

  const resync = useMutation({
    mutationFn: () => koboApi.resync(),
    onSuccess: (r) => {
      setResynced(r.forgotten)
      setError('')
    },
    onError: (e) => setError((e as Error).message),
  })

  return (
    <div>
      {error && <div className="error">{error}</div>}

      <p className="hint" style={{ marginTop: 0 }}>
        Each device gets its own token. Books reach it from any shelf you mark
        <strong> Sync to Kobo</strong> on the Shelves tab.
      </p>

      <form
        style={{ display: 'flex', gap: 8, margin: '14px 0 20px' }}
        onSubmit={(e) => {
          e.preventDefault()
          create.mutate(label.trim() || 'Kobo')
        }}
      >
        <input
          placeholder="Device name, e.g. Klara's Libra"
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          style={{
            flex: 1, height: 34, padding: '0 10px',
            border: '1px solid var(--border-strong)', borderRadius: 'var(--radius-sm)',
          }}
        />
        <button className="btn" type="submit" disabled={create.isPending}>
          {create.isPending ? 'Creating…' : 'Add a device'}
        </button>
      </form>

      {fresh && (
        <div className="callout">
          <strong>Set this as the device's <code>api_store</code></strong>
          <p className="hint" style={{ margin: '6px 0' }}>
            On the Kobo, edit <code>.kobo/Kobo/Kobo eReader.conf</code> and set
            <code> api_store</code> to exactly this, then sync.
          </p>
          <div className="copyrow">
            <code>{fresh.api_store_url}</code>
            <button
              className="btn btn--sm"
              onClick={async () => {
                try {
                  await navigator.clipboard.writeText(fresh.api_store_url)
                  setCopied(true)
                  setTimeout(() => setCopied(false), 2000)
                } catch {
                  // Clipboard needs a secure context; the text is selectable anyway.
                  setCopied(false)
                }
              }}
            >
              {copied ? 'Copied' : 'Copy'}
            </button>
          </div>
          {fresh.api_store_url.includes('YOUR-PUBLIC-URL') && (
            <p className="warn">
              KLARAS_EXTERNAL_URL is not set, so this URL is a placeholder. Set it in
              your compose file to the public https address the device can reach.
            </p>
          )}
        </div>
      )}

      {isLoading ? (
        <p>Loading…</p>
      ) : data?.tokens?.length ? (
        <div className="table-wrap">
          <table className="table">
          <thead>
            <tr>
              <th>Device</th>
              <th style={{ width: 150 }}>Added</th>
              <th style={{ width: 170 }}>Last sync</th>
            </tr>
          </thead>
          <tbody>
            {data.tokens.map((t) => (
              <tr key={t.id}>
                <td>{t.label || 'Kobo'}</td>
                <td>{new Date(t.created_at).toLocaleDateString('sv-SE')}</td>
                <td>
                  {t.last_used_at
                    ? new Date(t.last_used_at).toLocaleString('sv-SE')
                    : <span style={{ color: 'var(--text-muted)' }}>never</span>}
                </td>
              </tr>
            ))}
          </tbody>
          </table>
        </div>
      ) : (
        <p className="hint">No devices paired yet.</p>
      )}

      <hr style={{ margin: '24px 0 16px', border: 0, borderTop: '1px solid var(--border)' }} />

      <h3 style={{ margin: '0 0 6px', fontSize: 15 }}>Send everything again</h3>
      <p className="hint" style={{ marginTop: 0 }}>
        Use this when a device syncs without errors but books do not arrive.
        Klaras Library keeps a record of what each device has been told, and if
        that record and the device disagree — after a factory reset, a restore
        from backup, or a sync that never finished — books can be described as
        already-owned and quietly skipped. This forgets the record, so the next
        sync offers every book on your Kobo shelves as new.
      </p>
      <p className="hint">
        It is safe: nothing is deleted and no metadata changes. The only cost is
        that the device downloads the books again, which takes a few minutes.
      </p>

      {resynced != null && (
        <div className="callout" style={{ marginBottom: 12 }}>
          Forgot {resynced} book{resynced === 1 ? '' : 's'}. Sync the device now
          — it will take longer than usual.
        </div>
      )}

      <button
        className="btn"
        onClick={() => {
          setResynced(null)
          resync.mutate()
        }}
        disabled={resync.isPending}
      >
        {resync.isPending ? 'Forgetting…' : 'Force a full resync'}
      </button>
    </div>
  )
}
