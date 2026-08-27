import { useSyncExternalStore } from 'react'

/**
 * The address bar is the application's state.
 *
 * Everything used to be component state with one anonymous history entry
 * pushed underneath it, which made Back mean "close whatever is open" and
 * nothing else. Going Authors → an author → Back did not return to Authors,
 * because there was no Authors to return to: the browser had never been told
 * it was anywhere. Forward could not work at all, nothing was linkable, and a
 * reload always landed on the whole library.
 *
 * So each view has an address, and the app renders whatever the address says.
 * Back and Forward then work because they are the browser's own, not an
 * imitation of them.
 */

const listeners = new Set<() => void>()

function emit() {
  for (const l of listeners) l()
}

function subscribe(fn: () => void) {
  listeners.add(fn)
  window.addEventListener('popstate', fn)
  return () => {
    listeners.delete(fn)
    window.removeEventListener('popstate', fn)
  }
}

// useSyncExternalStore compares snapshots by identity, so this has to be a
// stable string rather than a fresh object each read.
const snapshot = () => window.location.pathname + window.location.search

/** The current path and query, re-rendering whenever either changes. */
export function useLocation(): { path: string; params: URLSearchParams } {
  const href = useSyncExternalStore(subscribe, snapshot, () => '/')
  const qi = href.indexOf('?')
  return {
    path: qi === -1 ? href : href.slice(0, qi),
    params: new URLSearchParams(qi === -1 ? '' : href.slice(qi)),
  }
}

/**
 * Go somewhere.
 *
 * `replace` is for changes that are not a place: typing in the search box
 * should not put a history entry between each keystroke and the page before it.
 */
export function navigate(to: string, opts?: { replace?: boolean }) {
  if (to === snapshot()) return
  if (opts?.replace) window.history.replaceState(null, '', to)
  else window.history.pushState(null, '', to)
  emit()
}

/** Back, or to the library if this tab has nowhere to go back to. */
export function goBack(fallback = '/') {
  if (window.history.length > 1) {
    window.history.back()
    return
  }
  navigate(fallback)
}

/** Build a path with a query string, dropping empty values. */
export function href(path: string, params: Record<string, unknown> = {}) {
  const p = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === '' || v === false) continue
    p.set(k, String(v))
  }
  const q = p.toString()
  return q ? `${path}?${q}` : path
}
