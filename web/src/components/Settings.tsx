import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { api, type User } from '../api'
import { SettingsKobo } from './SettingsKobo'
import { SettingsUsers } from './SettingsUsers'
import { Shelves } from './Shelves'

type Tab = 'kobo' | 'shelves' | 'users' | 'account'

export function Settings({
  user, onClose, onBrowseShelf,
}: {
  user: User
  onClose: () => void
  onBrowseShelf: (shelfId: number) => void
}) {
  const [tab, setTab] = useState<Tab>('kobo')
  const isAdmin = user.role === 'admin'

  const tabs: { id: Tab; label: string; show: boolean }[] = [
    { id: 'kobo', label: 'Kobo devices', show: true },
    { id: 'shelves', label: 'Shelves', show: true },
    { id: 'users', label: 'Users', show: isAdmin },
    { id: 'account', label: 'Your account', show: true },
  ]

  return (
    <div className="settings">
      <div className="settings__head">
        <h1>Settings</h1>
        <button className="btn btn--ghost" onClick={onClose}>
          ← Back to library
        </button>
      </div>

      <div className="settings__tabs" role="tablist">
        {tabs.filter((t) => t.show).map((t) => (
          <button
            key={t.id}
            role="tab"
            aria-selected={tab === t.id}
            className={`settings__tab ${tab === t.id ? 'settings__tab--active' : ''}`}
            onClick={() => setTab(t.id)}
          >
            {t.label}
          </button>
        ))}
      </div>

      <div className="settings__body">
        {tab === 'kobo' && <SettingsKobo />}
        {tab === 'shelves' && (
          <Shelves
            onBrowse={(id) => {
              onBrowseShelf(id)
              onClose()
            }}
          />
        )}
        {tab === 'users' && isAdmin && <SettingsUsers me={user} />}
        {tab === 'account' && <AccountTab user={user} />}
      </div>
    </div>
  )
}

function AccountTab({ user }: { user: User }) {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [done, setDone] = useState(false)
  const [error, setError] = useState('')

  const change = useMutation({
    mutationFn: () => api.changePassword(current, next),
    onSuccess: () => {
      setDone(true)
      setCurrent('')
      setNext('')
      setError('')
    },
    onError: (e) => setError((e as Error).message),
  })

  return (
    <div style={{ maxWidth: 420 }}>
      <dl className="kv" style={{ marginBottom: 22 }}>
        <dt>Username</dt>
        <dd>{user.username}</dd>
        {user.email && (<><dt>Email</dt><dd>{user.email}</dd></>)}
        <dt>Role</dt>
        <dd>{user.role}</dd>
      </dl>

      <h3>Change your password</h3>
      {error && <div className="error">{error}</div>}
      {done && <div className="callout">Password updated.</div>}
      <form
        onSubmit={(e) => {
          e.preventDefault()
          change.mutate()
        }}
      >
        <div className="field">
          <label htmlFor="cp">Current password</label>
          <input id="cp" type="password" value={current}
                 onChange={(e) => setCurrent(e.target.value)} autoComplete="current-password" />
        </div>
        <div className="field">
          <label htmlFor="npw">New password (at least 10 characters)</label>
          <input id="npw" type="password" value={next}
                 onChange={(e) => setNext(e.target.value)} autoComplete="new-password" />
        </div>
        <button className="btn" type="submit" disabled={next.length < 10 || change.isPending}>
          {change.isPending ? 'Saving…' : 'Change password'}
        </button>
      </form>
    </div>
  )
}
