import { useEffect, useRef } from 'react'

// Keep keyboard focus inside an open dialog and return it to its launcher.
export function useDialog(open, onClose) {
  const ref = useRef(null)
  const close = useRef(onClose)
  close.current = onClose

  useEffect(() => {
    if (!open) return
    const previous = document.activeElement
    const controls = () => [...ref.current.querySelectorAll('button, input, select, textarea, a[href], [tabindex]')]
      .filter((el) => !el.disabled && el.tabIndex >= 0 && el.getClientRects().length > 0)
    if (!ref.current.contains(document.activeElement)) controls()[0]?.focus()

    const onKey = (event) => {
      if (event.key === 'Escape' && close.current) {
        event.preventDefault()
        close.current()
      }
      if (event.key !== 'Tab') return
      const items = controls()
      const first = items[0]
      const last = items[items.length - 1]
      if (!ref.current.contains(document.activeElement) || (event.shiftKey ? document.activeElement === first : document.activeElement === last)) {
        event.preventDefault()
        const target = event.shiftKey ? last : first
        target?.focus()
      }
    }
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('keydown', onKey)
      if (previous?.isConnected) previous.focus()
    }
  }, [open])

  return ref
}
