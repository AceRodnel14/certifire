import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'

export const errMsg = (e: unknown): string => (e instanceof Error ? e.message : 'Something went wrong')

type IconName = 'copy' | 'download' | 'trash' | 'renew' | 'edit' | 'x' | 'plus' | 'check' | 'logout' | 'user' | 'shield' | 'alert'
const PATHS: Record<IconName, string> = {
  copy: 'M9 9h10v12H9zM5 15V3h10',
  download: 'M12 3v12m0 0l-4-4m4 4l4-4M4 21h16',
  trash: 'M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3',
  renew: 'M20 11a8 8 0 10-2.3 5.7M20 4v7h-7',
  edit: 'M4 20h4L19 9l-4-4L4 16zM13 7l4 4',
  x: 'M6 6l12 12M18 6L6 18',
  plus: 'M12 5v14M5 12h14',
  check: 'M5 13l4 4L19 7',
  logout: 'M15 4h4v16h-4M10 8l-4 4 4 4M6 12h10',
  user: 'M12 12a4 4 0 100-8 4 4 0 000 8zM4 21a8 8 0 0116 0',
  shield: 'M12 3l8 3v6c0 5-3.5 8-8 9-4.5-1-8-4-8-9V6z',
  alert: 'M12 4l9 16H3zM12 10v4M12 17v.5',
}
export function Icon({ name, size = 16 }: { name: IconName; size?: number }) {
  return (
    <svg className="icon" width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d={PATHS[name]} />
    </svg>
  )
}

export function Spinner({ label = 'Loading' }: { label?: string }) {
  return (
    <div className="center-note" role="status">
      <span className="spinner" aria-hidden="true" /> {label}...
    </div>
  )
}

export function ErrorNote({ error, onRetry }: { error: string; onRetry?: () => void }) {
  return (
    <div className="note error" role="alert">
      <Icon name="alert" /> <span>{error}</span>
      {onRetry && (
        <button type="button" className="btn small" onClick={onRetry}>
          Retry
        </button>
      )}
    </div>
  )
}

type Loaded<T> = { token: number; data?: T; error?: string }

/** Load data on mount (and on reload()). `fn` must be stable (useCallback). */
export function useLoad<T>(fn: () => Promise<T>) {
  const [token, setToken] = useState(0)
  const [res, setRes] = useState<Loaded<T> | null>(null)
  useEffect(() => {
    let live = true
    fn().then(
      (data) => live && setRes({ token, data }),
      (e: unknown) => live && setRes({ token, error: errMsg(e) }),
    )
    return () => {
      live = false
    }
  }, [fn, token])
  const reload = useCallback(() => setToken((t) => t + 1), [])
  const fresh = res !== null && res.token === token
  return {
    data: res?.data,
    error: fresh ? res.error : undefined,
    loading: !fresh && res?.data === undefined,
    reload,
  }
}

/** Run an async action with busy/error state. */
export function useAction() {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const run = useCallback(async <T,>(fn: () => Promise<T>): Promise<T | undefined> => {
    setBusy(true)
    setError(null)
    try {
      return await fn()
    } catch (e) {
      setError(errMsg(e))
      return undefined
    } finally {
      setBusy(false)
    }
  }, [])
  return { busy, error, setError, run }
}

/** Copy via the async clipboard API, or a temporary textarea + execCommand on plain HTTP. */
async function copyText(text: string): Promise<boolean> {
  if (window.isSecureContext && navigator.clipboard) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      /* fall through to the legacy path */
    }
  }
  const ta = document.createElement('textarea')
  ta.value = text
  ta.setAttribute('readonly', '')
  ta.className = 'copy-buffer'
  document.body.appendChild(ta)
  ta.select()
  let ok: boolean
  try {
    ok = document.execCommand('copy')
  } catch {
    ok = false
  }
  if (!ok) {
    // Leave it selected so the user can press Ctrl+C.
    ta.className = 'copy-buffer visible'
    ta.focus()
    ta.select()
    window.setTimeout(() => ta.remove(), 6000)
  } else ta.remove()
  return ok
}

export function CopyButton({ text, label = 'Copy' }: { text: string; label?: string }) {
  const [state, setState] = useState<'idle' | 'done' | 'fail'>('idle')
  const timer = useRef<number | undefined>(undefined)
  useEffect(() => () => window.clearTimeout(timer.current), [])
  async function copy() {
    const ok = await copyText(text)
    setState(ok ? 'done' : 'fail')
    window.clearTimeout(timer.current)
    timer.current = window.setTimeout(() => setState('idle'), ok ? 1800 : 6000)
  }
  return (
    <button type="button" className="btn small" onClick={() => void copy()}>
      <Icon name={state === 'done' ? 'check' : 'copy'} />
      {state === 'done' ? 'Copied' : state === 'fail' ? 'Press Ctrl+C' : label}
    </button>
  )
}

export function Modal({ title, onClose, children }: { title: string; onClose: () => void; children: ReactNode }) {
  const ref = useRef<HTMLDialogElement>(null)
  useEffect(() => {
    const d = ref.current
    if (d && !d.open) d.showModal()
  }, [])
  return (
    <dialog ref={ref} className="modal" onCancel={(e) => { e.preventDefault(); onClose() }} onClick={(e) => e.target === ref.current && onClose()}>
      <header className="modal-head">
        <h2>{title}</h2>
        <button type="button" className="modal-close" aria-label="Close" onClick={onClose}><Icon name="x" /></button>
      </header>
      <div className="modal-body">{children}</div>
    </dialog>
  )
}

export function ConfirmDialog(props: {
  title: string
  message: ReactNode
  confirmLabel: string
  onConfirm: () => Promise<void>
  onClose: () => void
}) {
  const { busy, error, run } = useAction()
  return (
    <Modal title={props.title} onClose={props.onClose}>
      <p>{props.message}</p>
      {error && <ErrorNote error={error} />}
      <div className="row end">
        <button type="button" className="btn" onClick={props.onClose} disabled={busy}>
          Cancel
        </button>
        <button type="button" className="btn danger solid" disabled={busy} onClick={() => void run(async () => { await props.onConfirm(); return true }).then((ok) => ok && props.onClose())}>
          {busy ? 'Working...' : props.confirmLabel}
        </button>
      </div>
    </Modal>
  )
}

export const fmtDate = (iso: string): string =>
  new Date(iso).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
