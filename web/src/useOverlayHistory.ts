import { useEffect } from 'react'

/**
 * Makes overlays respond to the browser's Back button.
 *
 * Settings, the book drawer, the editor and the reader are component state, not
 * routes, so Back used to leave the site entirely rather than closing them --
 * jarring on a desktop and wrong on a phone, where Back is the first gesture
 * people reach for.
 *
 * The history entry is shared by every overlay rather than owned per overlay,
 * because overlays hand off to each other. "Read" inside the book drawer closes
 * the drawer and opens the reader in a single React commit, and with an entry
 * each that sequence was: drawer cleanup calls history.back(), reader mounts and
 * calls pushState, then the back() -- which is asynchronous -- lands as a
 * popstate on the reader's freshly attached listener and closes it immediately.
 * The drawer vanished and the reader never appeared.
 *
 * One shared entry makes a hand-off invisible to history: the count never
 * reaches zero, so nothing is pushed or popped. Back closes whatever is open.
 */

// Module scope, not component scope: the whole point is that these outlive any
// single overlay's mount.
let openCount = 0
let pushed = false
let checkQueued = false
const closers = new Set<() => void>()

function onPop() {
  // Our entry is already gone -- Back consumed it. Do not push it back.
  pushed = false
  detach()
  // Copy first: closing mutates the set through the unmount effects.
  for (const close of [...closers]) close()
}

function attach() {
  window.addEventListener('popstate', onPop)
}

function detach() {
  window.removeEventListener('popstate', onPop)
}

/**
 * Settles history after the commit rather than during it.
 *
 * React runs every cleanup and every effect for a commit synchronously, so
 * during a hand-off the count dips to zero and back to one. Deferring means we
 * only ever see the settled count, and a hand-off touches history not at all.
 */
function scheduleCheck() {
  if (checkQueued) return
  checkQueued = true
  queueMicrotask(() => {
    checkQueued = false
    if (openCount > 0 && !pushed) {
      window.history.pushState({ klarasOverlay: true }, '')
      pushed = true
      attach()
    } else if (openCount === 0 && pushed) {
      // Closed by a button or Escape, so take our entry back out rather than
      // leaving a step that does nothing visible.
      pushed = false
      detach()
      window.history.back()
    }
  })
}

export function useOverlayHistory(open: boolean, onClose: () => void) {
  useEffect(() => {
    if (!open) return

    // Registered fresh on every render so Back always calls the current
    // closure, without the effect itself depending on it -- re-running the
    // effect would churn the count.
    const close = () => onClose()
    closers.add(close)
    openCount++
    scheduleCheck()

    return () => {
      closers.delete(close)
      openCount--
      scheduleCheck()
    }
  })
}
