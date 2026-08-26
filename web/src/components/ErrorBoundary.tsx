import React from 'react'

/**
 * Catches a render crash and shows what happened.
 *
 * Without this, one bad value anywhere in the tree unmounts the whole app and
 * leaves a white page: no message, nothing in the interface to report, and
 * nothing to do but reload and hope. That is precisely how a lookup returning
 * no results presented itself. The message here is not for the person to fix
 * anything -- it is so the failure can be described instead of guessed at.
 */
export class ErrorBoundary extends React.Component<
  { children: React.ReactNode },
  { error: Error | null }
> {
  state: { error: Error | null } = { error: null }

  static getDerivedStateFromError(error: Error) {
    return { error }
  }

  componentDidCatch(error: Error, info: React.ErrorInfo) {
    console.error('render failed', error, info.componentStack)
  }

  render() {
    if (!this.state.error) return this.props.children
    return (
      <div className="crash">
        <h1>Something in the page broke</h1>
        <p>
          The rest of the library is fine — this is a fault in the interface, not in
          your books. Reloading usually gets you moving again.
        </p>
        <p className="crash__what">{this.state.error.message}</p>
        <div className="crash__row">
          <button className="btn" onClick={() => window.location.reload()}>
            Reload
          </button>
          <button
            className="btn btn--ghost"
            onClick={() => void navigator.clipboard?.writeText(
              `${this.state.error?.message}\n\n${this.state.error?.stack ?? ''}`,
            )}
          >
            Copy details
          </button>
        </div>
      </div>
    )
  }
}
