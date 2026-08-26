import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createUser, koboApi, usersApi, type UserSummary } from '../api'

const ROLES = [
  { value: 'reader', label: 'Reader', hint: 'browse, read and download' },
  { value: 'editor', label: 'Editor', hint: 'also edit metadata' },
  { value: 'admin', label: 'Admin', hint: 'also manage users and settings' },
]

export function SettingsUsers({ me }: { me: { id: number } }) {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({ queryKey: ['users'], queryFn: usersApi.list })
  const [pwFor, setPwFor] = useState<UserSummary | null>(null)
  const [adding, setAdding] = useState(false)
  const [error, setError] = useState('')
  const [resynced, setResynced] = useState<{ n: number; username: string } | null>(null)

  const refresh = () => void qc.invalidateQueries({ queryKey: ['users'] })
  const update = useMutation({
    mutationFn: (v: { id: number; patch: { role?: string; is_active?: boolean } }) =>
      usersApi.update(v.id, v.patch),
    onSuccess: () => { setError(''); refresh() },
    onError: (e) => setError((e as Error).message),
  })

  // calibre-web's "force full Kobo sync", for someone else's device. The
  // person holding the Kobo is rarely the person with the server open.
  const resync = useMutation({
    mutationFn: (id: number) => koboApi.resync(id).then((r) => ({ r, id })),
    onSuccess: ({ r, id }) => {
      setError('')
      setResynced({
        n: r.forgotten,
        username: data?.users.find((u) => u.id === id)?.username ?? 'that account',
      })
    },
    onError: (e) => setError((e as Error).message),
  })

  if (isLoading) return <p>Loading…</p>

  const needing = data?.users?.filter((u) => u.needs_password) ?? []

  return (
    <div>
      {error && <div className="error">{error}</div>}

      {needing.length > 0 && (
        <div className="callout callout--warn">
          <strong>
            {needing.length} account{needing.length > 1 ? 's' : ''} cannot sign in yet
          </strong>
          <p className="hint" style={{ margin: '4px 0 0' }}>
            They came from calibre-web, whose password format cannot be converted.
            Set a password for each below.
          </p>
        </div>
      )}

      <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 12 }}>
        <button className="btn" onClick={() => setAdding(true)}>Add user</button>
      </div>

      {resynced && (
        <div className="callout" style={{ marginBottom: 12 }}>
          Forgot {resynced.n} book{resynced.n === 1 ? '' : 's'} for{' '}
          <strong>{resynced.username}</strong>. Their Kobo will offer everything
          again on the next sync, which will take longer than usual.
        </div>
      )}

      <div className="table-wrap">
        <table className="table">
        <thead>
          <tr>
            <th>User</th>
            <th style={{ width: 130 }}>Role</th>
            <th style={{ width: 90 }}>Shelves</th>
            <th style={{ width: 80 }}>Kobo</th>
            <th style={{ width: 110 }}>Active</th>
            <th style={{ width: 150 }}></th>
          </tr>
        </thead>
        <tbody>
          {data?.users?.map((u) => (
            <tr key={u.id} className={u.needs_password ? 'row--warn' : undefined}>
              <td>
                <strong>{u.username}</strong>
                {u.id === me.id && <span className="badge">you</span>}
                {u.email && <div className="sub">{u.email}</div>}
                {u.needs_password && <div className="warn">no password set</div>}
              </td>
              <td>
                <select
                  className="sort"
                  value={u.role}
                  onChange={(e) => update.mutate({ id: u.id, patch: { role: e.target.value } })}
                  title={ROLES.find((r) => r.value === u.role)?.hint}
                >
                  {ROLES.map((r) => (
                    <option key={r.value} value={r.value}>{r.label}</option>
                  ))}
                </select>
              </td>
              <td>{u.shelves}</td>
              <td>
                {u.kobo_tokens > 0 ? (
                  <button
                    className="btn btn--sm btn--ghost"
                    title={
                      'Forget what this account\u2019s Kobo devices have been told, ' +
                      'so the next sync offers every book again. Use it when a device ' +
                      'syncs without errors but books do not arrive.'
                    }
                    disabled={resync.isPending}
                    onClick={() => {
                      setResynced(null)
                      resync.mutate(u.id)
                    }}
                  >
                    {u.kobo_tokens} · resync
                  </button>
                ) : (
                  u.kobo_tokens
                )}
              </td>
              <td>
                <label className="switch">
                  <input
                    type="checkbox"
                    checked={u.is_active}
                    onChange={(e) =>
                      update.mutate({ id: u.id, patch: { is_active: e.target.checked } })
                    }
                  />
                  <span>{u.is_active ? 'Active' : 'Disabled'}</span>
                </label>
              </td>
              <td style={{ textAlign: 'right' }}>
                <button className="btn btn--sm btn--ghost" onClick={() => setPwFor(u)}>
                  Set password
                </button>
              </td>
            </tr>
          ))}
        </tbody>
        </table>
      </div>

      {adding && (
        <NewUserDialog
          onClose={() => setAdding(false)}
          onDone={() => { setAdding(false); refresh() }}
        />
      )}

      {pwFor && (
        <PasswordDialog
          user={pwFor}
          onClose={() => setPwFor(null)}
          onDone={() => { setPwFor(null); refresh() }}
        />
      )}
    </div>
  )
}

function NewUserDialog({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [username, setUsername] = useState('')
  const [email, setEmail] = useState('')
  const [pw, setPw] = useState('')
  const [role, setRole] = useState('reader')
  const [error, setError] = useState('')

  const create = useMutation({
    mutationFn: () => createUser(username.trim(), email.trim(), pw, role),
    onSuccess: onDone,
    onError: (e) => setError((e as Error).message),
  })

  return (
    <>
      <div className="drawer-backdrop" onClick={onClose} />
      <div className="dialog" role="dialog" aria-modal="true" aria-label="Add a user">
        <h3 style={{ marginTop: 0 }}>Add a user</h3>
        {error && <div className="error">{error}</div>}
        <form onSubmit={(e) => { e.preventDefault(); create.mutate() }}>
          <div className="field">
            <label htmlFor="nu">Username</label>
            <input id="nu" value={username} onChange={(e) => setUsername(e.target.value)}
                   autoFocus autoComplete="off" required />
          </div>
          <div className="field">
            <label htmlFor="ne">Email (optional)</label>
            <input id="ne" type="email" value={email}
                   onChange={(e) => setEmail(e.target.value)} autoComplete="off" />
          </div>
          <div className="field">
            <label htmlFor="nr">Role</label>
            <select id="nr" className="sort" style={{ width: '100%', height: 36 }}
                    value={role} onChange={(e) => setRole(e.target.value)}>
              {ROLES.map((r) => (
                <option key={r.value} value={r.value}>{r.label} — {r.hint}</option>
              ))}
            </select>
          </div>
          <div className="field">
            <label htmlFor="nup">Password (at least 10 characters)</label>
            <input id="nup" type="password" value={pw}
                   onChange={(e) => setPw(e.target.value)} autoComplete="new-password" />
          </div>
          <div style={{ display: 'flex', gap: 8 }}>
            <button className="btn" type="submit"
                    disabled={!username.trim() || pw.length < 10 || create.isPending}>
              {create.isPending ? 'Creating…' : 'Create user'}
            </button>
            <button className="btn btn--ghost" type="button" onClick={onClose}>Cancel</button>
          </div>
        </form>
      </div>
    </>
  )
}

function PasswordDialog({
  user, onClose, onDone,
}: {
  user: UserSummary
  onClose: () => void
  onDone: () => void
}) {
  const [pw, setPw] = useState('')
  const [error, setError] = useState('')
  const save = useMutation({
    mutationFn: () => usersApi.setPassword(user.id, pw),
    onSuccess: onDone,
    onError: (e) => setError((e as Error).message),
  })

  return (
    <>
      <div className="drawer-backdrop" onClick={onClose} />
      <div className="dialog" role="dialog" aria-modal="true" aria-label="Set password">
        <h3 style={{ marginTop: 0 }}>Password for {user.username}</h3>
        {error && <div className="error">{error}</div>}
        <form
          onSubmit={(e) => {
            e.preventDefault()
            save.mutate()
          }}
        >
          <div className="field">
            <label htmlFor="np">New password (at least 10 characters)</label>
            <input
              id="np"
              type="password"
              value={pw}
              onChange={(e) => setPw(e.target.value)}
              autoFocus
              autoComplete="new-password"
            />
          </div>
          <div style={{ display: 'flex', gap: 8 }}>
            <button className="btn" type="submit" disabled={pw.length < 10 || save.isPending}>
              {save.isPending ? 'Saving…' : 'Set password'}
            </button>
            <button className="btn btn--ghost" type="button" onClick={onClose}>
              Cancel
            </button>
          </div>
        </form>
      </div>
    </>
  )
}
