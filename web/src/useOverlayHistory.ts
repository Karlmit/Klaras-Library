import { useEffect, useRef } from 'react'

/**
 * Makes an overlay respond to the browser's Back button.
 *
 * Settings, the book drawer and the reader are component state, not routes, so
 * Back used to leave the site entirely rather than closing them — which is
 * jarring, and on a phone it is the gesture people reach for first.
 *
 * Opening pushes a history entry; Back pops it and calls onClose. Closing by
 * other means (a button, Escape) removes that entry again, so the history does
 * not fill with dead states someone has to press Back through twice.
 */
export function useOverlayHistory(open: boolean, onClose: () => void, key: string) {
  // The callback is held in a ref so the effect depends only on `open`;
  // otherwise a new closure each render would push a fresh entry every time.
  const closeRef = useRef(onClose)
  closeRef.current = onClose

  const pushedRef = useRef(false)

  useEffect(() => {
    if (!open) return

    window.history.pushState({ klarasOverlay: key }, '')
    pushedRef.current = true

    const onPop = () => {
      // The entry is already gone: Back consumed it.
      pushedRef.current = false
      closeRef.current()
    }
    window.addEventListener('popstate', onPop)

    return () => {
      window.removeEventListener('popstate', onPop)
      // Closed without Back, so take our entry back out rather than leaving a
      // step that does nothing visible.
      if (pushedRef.current) {
        pushedRef.current = false
        window.history.back()
      }
    }
  }, [open, key])
}
