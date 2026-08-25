import { useState } from 'react'
import { api, ApiError, type User } from '../api'
import { BrandLogo } from './Brand'

interface Props {
  mode: 'login' | 'setup'
  onDone: (u: User) => void
}

/** Login and first-run setup share a layout; only the fields differ. */
export function AuthScreen({ mode, onDone }: Props) {
  const [username, setUsername] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      const u =
        mode === 'setup'
          ? await api.setup(username, email, password)
          : await api.login(username, password)
      onDone(u)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Something went wrong')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="auth">
      <form className="auth__card" onSubmit={submit}>
        <BrandLogo />
        <h1>Klaras Library</h1>
        <p className="sub">
          {mode === 'setup' ? 'Create the first administrator account' : 'Sign in to continue'}
        </p>

        {error && <div className="error">{error}</div>}

        <div className="field">
          <label htmlFor="u">Username</label>
          <input
            id="u"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            autoComplete="username"
            autoFocus
            required
          />
        </div>

        {mode === 'setup' && (
          <div className="field">
            <label htmlFor="e">Email (optional)</label>
            <input
              id="e"
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              autoComplete="email"
            />
          </div>
        )}

        <div className="field">
          <label htmlFor="p">
            Password{mode === 'setup' && ' (at least 10 characters)'}
          </label>
          <input
            id="p"
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete={mode === 'setup' ? 'new-password' : 'current-password'}
            required
          />
        </div>

        <button className="btn" style={{ width: '100%' }} disabled={busy} type="submit">
          {busy ? 'Working…' : mode === 'setup' ? 'Create account' : 'Sign in'}
        </button>
      </form>
    </div>
  )
}

/** Shown to users imported from calibre-web, whose password hash cannot be converted. */
export function ForcePasswordChange({ onDone }: { onDone: () => void }) {
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      await api.changePassword('', password)
      onDone()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Something went wrong')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="auth">
      <form className="auth__card" onSubmit={submit}>
        <BrandLogo />
        <h1>Set a new password</h1>
        <p className="sub">
          Your account came across from calibre-web. Its password format cannot be
          converted, so please choose a new one.
        </p>
        {error && <div className="error">{error}</div>}
        <div className="field">
          <label htmlFor="np">New password (at least 10 characters)</label>
          <input
            id="np"
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="new-password"
            autoFocus
            required
          />
        </div>
        <button className="btn" style={{ width: '100%' }} disabled={busy} type="submit">
          {busy ? 'Saving…' : 'Save password'}
        </button>
      </form>
    </div>
  )
}
