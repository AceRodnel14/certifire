import { useEffect, useId, useRef, useState } from 'react'

import { tipOpen, type TipEvent } from '../tip'

/** Small "i" button with a tooltip: shows on hover, keyboard focus and tap; Escape closes it. */
export function InfoTip({ text, label = 'More info' }: { text: string; label?: string }) {
  const id = useId()
  const [open, setOpen] = useState(false)
  // Hover events only count for a real mouse: on touch the emulated enter/leave around a tap
  // would close the tip right after the click opened it. The tooltip is positioned against the surrounding label row / fieldset (CSS), so it never
  // extends past the field's width, even at 360 px.
  const ref = useRef<HTMLSpanElement>(null)
  const send = (ev: TipEvent) => setOpen(tipOpen(ev))

  // Escape dismisses the tip wherever the focus or pointer is (WCAG 1.4.13); inside a dialog it
  // closes only the tip because the event is stopped before the dialog sees it.
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      e.preventDefault()
      e.stopPropagation()
      send('escape')
    }
    const onPress = (e: Event) => {
      if (!ref.current?.contains(e.target as Node)) send('outside')
    }
    document.addEventListener('keydown', onKey, true)
    document.addEventListener('pointerdown', onPress, true)
    return () => {
      document.removeEventListener('keydown', onKey, true)
      document.removeEventListener('pointerdown', onPress, true)
    }
  }, [open])

  return (
    <span ref={ref} className="tip" onPointerEnter={(e) => e.pointerType === 'mouse' && send('enter')} onPointerLeave={(e) => e.pointerType === 'mouse' && send('leave')}>
      <button
        type="button"
        className="tip-btn"
        aria-label={label}
        aria-describedby={id}
        aria-expanded={open}
        onFocus={() => send('focus')}
        onBlur={() => send('blur')}
        onClick={() => send('click')}
      >
        i
      </button>
      <span id={id} role="tooltip" className={`tip-text${open ? ' open' : ''}`}>
        {text}
      </span>
    </span>
  )
}
