import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { koboApi } from '../api'

// Existing device URLs are available to their signed-in owner for recovery.
export function SettingsKobo() {
  const qc = useQueryClient()
  const { data, isLoading, error: loadError } = useQuery({ queryKey: ['kobo-tokens'], queryFn: koboApi.tokens })
  const [label, setLabel] = useState('')
  const [fresh, setFresh] = useState<{ token: string; api_store_url: string } | null>(null)
  const [copied, setCopied] = useState<string | null>(null)
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
      {loadError && <div className="error">Could not load existing devices: {loadError.message}</div>}

      <p className="hint" style={{ marginTop: 0 }}>
        Each device gets its own token. Books reach it from any shelf you mark
        <strong> Sync to Kobo</strong> on the Shelves tab.
      </p>

      <div className="callout">
        <h3 style={{ marginTop: 0 }}>Set up or restore Kobo sync</h3>
        <p className="hint">
          After an update or reset, reuse the URL for your existing device below.
          You only need Add a device when pairing another eReader.
        </p>
        <ol>
          <li>Connect the Kobo to your computer by USB and choose Connect on the Kobo.</li>
          <li>Show hidden files and back up <code>.kobo/Kobo/Kobo eReader.conf</code> from the Kobo drive.</li>
          <li>Open that file in a plain text editor. In the <code>[OneStoreServices]</code> section,
            replace the <code>api_endpoint=</code> line with the complete line shown below for your device.
            If it is missing, add it directly under <code>[OneStoreServices]</code>. Keep only one
            <code> api_endpoint</code> entry and leave the other settings in place.
            Remove any <code>api_store=</code> line added using the earlier instructions;
            Kobo uses <code>api_endpoint</code> for this setting.</li>
          <li>Save the file, safely eject the Kobo, disconnect USB, then sync over Wi-Fi.</li>
        </ol>
        <p className="hint">
          Check that your shelves have <strong>Sync to Kobo</strong> enabled.
          If books are still missing after restoring the URL, use <strong>Force a full resync</strong>
          {' '}below, then sync the Kobo again. This resets delivery records for all devices on your account.
          If the device still does not connect, check its Last sync time below after syncing.
          An unchanged time means the request has not authenticated with this library.
          Keep device URLs private: they grant access to your synced library.
        </p>
      </div>

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
          <strong>Set this as the device's <code>api_endpoint</code></strong>
          <p className="hint" style={{ margin: '6px 0' }}>
            On the Kobo, edit <code>.kobo/Kobo/Kobo eReader.conf</code> and set
            <code> api_endpoint</code> to the URL in this line, then sync.
          </p>
          <div className="copyrow">
            <code style={{ overflowWrap: 'anywhere', whiteSpace: 'normal' }}>api_endpoint={fresh.api_store_url}</code>
            <button
              className="btn btn--sm"
              onClick={async () => {
                try {
                  await navigator.clipboard.writeText(`api_endpoint=${fresh.api_store_url}`)
                  setCopied(fresh.api_store_url)
                  setTimeout(() => setCopied(null), 2000)
                } catch {
                  // Clipboard needs a secure context; the text is selectable anyway.
                  setCopied(null)
                }
              }}
            >
              {copied === fresh.api_store_url ? 'Copied' : 'Copy line'}
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
      ) : loadError ? null : data?.tokens?.length ? (
        <div className="table-wrap">
          <table className="table">
          <thead>
            <tr>
              <th>Device / sync configuration</th>
              <th style={{ width: 150 }}>Added</th>
              <th style={{ width: 170 }}>Last sync</th>
            </tr>
          </thead>
          <tbody>
            {data.tokens.map((t) => (
              <tr key={t.id}>
                <td>
                  <strong>{t.label || 'Kobo'}</strong>
                  <div className="copyrow" style={{ marginTop: 6 }}>
                    <code style={{ overflowWrap: 'anywhere', whiteSpace: 'normal' }}>api_endpoint={t.api_store_url}</code>
                    <button className="btn btn--sm" onClick={async () => {
                      try {
                        await navigator.clipboard.writeText(`api_endpoint=${t.api_store_url}`)
                        setCopied(t.api_store_url)
                        setTimeout(() => setCopied(null), 2000)
                      } catch {
                        setError('Could not copy automatically. Select and copy the configuration line above.')
                      }
                    }}>
                      {copied === t.api_store_url ? 'Copied' : 'Copy line'}
                    </button>
                  </div>
                  {t.api_store_url.includes('YOUR-PUBLIC-URL') && (
                    <p className="warn">Set KLARAS_EXTERNAL_URL to the public HTTPS address your Kobo can reach before using this line.</p>
                  )}
                </td>
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
