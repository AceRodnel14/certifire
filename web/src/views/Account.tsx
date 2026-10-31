import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'

import * as api from '../api'
import { InfoTip } from '../components/InfoTip'
import { ErrorNote, Spinner, useAction, useLoad } from '../components/ui'
import { applyTheme, cacheTheme, currentTheme, DEFAULT_HUE, normalizeTheme } from '../theme'
import { checkPassword } from '../validate'

const MODES: { value: api.ThemeMode; label: string }[] = [
  { value: 'auto', label: 'Auto' },
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
]
const STOPS = Array.from({ length: 13 }, (_, i) => `hsl(${i * 30} 85% 50%)`).join(', ')

function Appearance() {
  // Start from the theme that is applied right now, not from the (possibly stale) state loaded at login.
  const [theme, setTheme] = useState(() => currentTheme())
  const [error, setError] = useState<string | null>(null)
  const saved = useRef(theme)
  const timer = useRef<number | undefined>(undefined)
  const seq = useRef(0)
  const sliderRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    // Draw the slider track gradient through the CSSOM (inline style attributes are blocked by the CSP).
    sliderRef.current?.style.setProperty('--stops', STOPS)
    return () => window.clearTimeout(timer.current)
  }, [])

  function change(patch: Partial<api.Theme>) {
    const next = normalizeTheme({ ...theme, ...patch })
    setTheme(next)
    applyTheme(next)
    cacheTheme(next)
    window.clearTimeout(timer.current)
    const mine = ++seq.current
    timer.current = window.setTimeout(() => {
      api.saveTheme(next).then(
        () => { saved.current = next; setError(null) },
        (e: unknown) => {
          if (mine !== seq.current) return
          setError(e instanceof Error ? e.message : 'Could not save the theme')
          setTheme(saved.current)
          applyTheme(saved.current)
          cacheTheme(saved.current)
        },
      )
    }, 300)
  }

  return (
    <div className="card stack">
      <h2>Appearance</h2>
      <div className="field">
        <span className="field-label" id="mode-label">Mode</span>
        <div className="seg" role="radiogroup" aria-labelledby="mode-label">
          {MODES.map((m) => (
            <button key={m.value} type="button" role="radio" aria-checked={theme.mode === m.value} className={theme.mode === m.value ? 'on' : ''} onClick={() => change({ mode: m.value })}>
              {m.label}
            </button>
          ))}
        </div>
        <span className="hint">Auto follows your device's light or dark setting.</span>
      </div>
      <div className="field">
        <label className="field-label" htmlFor="hue">Accent colour</label>
        <input ref={sliderRef} id="hue" className="hue" type="range" min={0} max={359} step={1} value={theme.hue ?? DEFAULT_HUE} onChange={(e) => change({ hue: Number(e.target.value) })} aria-valuetext={theme.hue === null ? 'Default' : `Hue ${theme.hue}`} />
        <div className="row between">
          <span className="hint">{theme.hue === null ? 'Default (flame orange)' : `Custom hue ${theme.hue}`}</span>
          <button type="button" className="btn small" disabled={theme.hue === null} onClick={() => change({ hue: null })}>Reset colour</button>
        </div>
      </div>
      {error && <ErrorNote error={error} />}
    </div>
  )
}

function ChangePassword() {
  const [current, setCurrent] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [done, setDone] = useState(false)
  const { busy, error, setError, run } = useAction()

  async function submit(e: FormEvent) {
    e.preventDefault()
    setDone(false)
    const bad = checkPassword(password, confirm)
    if (bad) return setError(bad)
    const r = await run(async () => { await api.changePassword(current, password, confirm); return true })
    if (r) {
      setDone(true)
      setCurrent(''); setPassword(''); setConfirm('')
    }
  }

  return (
    <form className="card stack" onSubmit={(e) => void submit(e)}>
      <h2>Change password</h2>
      <label className="field"><span className="field-label">Current password</span>
        <input className="input" type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} required /></label>
      <label className="field"><span className="field-label">New password</span>
        <input className="input" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} required /></label>
      <label className="field"><span className="field-label">Confirm new password</span>
        <input className="input" type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} required /></label>
      {error && <ErrorNote error={error} />}
      {done && <div className="note ok" role="status">Password changed. Your other sessions were signed out.</div>}
      <div><button className="btn primary" disabled={busy}>{busy ? 'Saving...' : 'Change password'}</button></div>
    </form>
  )
}

const KIND_LABEL: Record<api.WebhookKind, string> = { discord: 'Discord', teams: 'Microsoft Teams' }
const KIND_HINT: Record<api.WebhookKind, string> = {
  discord: 'Channel settings, Integrations, Webhooks, Copy Webhook URL.',
  teams: "Teams Workflows app, 'Send webhook alerts to a channel' (trigger: Anyone), copy the URL.",
}
const TEAMS_HINT = 'Teams accepted the request, but it gives no delivery confirmation. If nothing appears in the channel, check the workflow run history in Power Automate.'

function WebhookCard({ kind, hook, onChanged }: { kind: api.WebhookKind; hook: api.Webhook | null; onChanged: () => void }) {
  const [url, setUrl] = useState('')
  const [result, setResult] = useState<api.TestOutcome | null>(null)
  const save = useAction()
  const remove = useAction()
  const test = useAction()

  async function onSave(e: FormEvent) {
    e.preventDefault()
    setResult(null)
    const r = await save.run(() => api.saveWebhook(kind, url.trim()))
    if (r) { setUrl(''); onChanged() }
  }
  async function onTest() {
    setResult(null)
    const r = await test.run(() => api.testWebhook(kind))
    if (r) { setResult(r); onChanged() }
  }
  async function onRemove() {
    setResult(null)
    const r = await remove.run(async () => { await api.deleteWebhook(kind); return true })
    if (r) onChanged()
  }

  const id = `hook-${kind}`
  return (
    <form className="card stack" onSubmit={(e) => void onSave(e)}>
      <div className="row between">
        <h3>{KIND_LABEL[kind]}</h3>
        {hook?.broken && <span className="badge bad">broken</span>}
        {hook && !hook.broken && <span className="badge ok">active</span>}
      </div>
      {hook && <p className="mono-line">{hook.masked}</p>}
      {hook?.broken && (
        <div className="note error" role="alert">
          {hook.brokenReason ?? 'Delivery failed permanently'}. Reminders are paused until you save the URL again or a test succeeds.
        </div>
      )}
      <div className="field">
        <div className="label-row">
          <label className="field-label" htmlFor={id}>{hook ? 'Replace webhook URL' : 'Webhook URL'}</label>
          <InfoTip text={'Paste the full URL. It is stored on the server and never shown again; only a masked form is displayed.'} label="Where to get the URL" />
        </div>
        <input id={id} className="input mono" type="password" autoComplete="off" spellCheck={false} placeholder={hook ? 'Paste a new URL to replace it' : 'https://...'} value={url} onChange={(e) => setUrl(e.target.value)} />
        <span className="hint">{KIND_HINT[kind]}</span>
      </div>
      {save.error && <ErrorNote error={save.error} />}
      {remove.error && <ErrorNote error={remove.error} />}
      {test.error && <ErrorNote error={test.error} />}
      {result && (
        <div className={`note ${result.outcome === 'delivered' ? 'ok' : result.outcome === 'accepted' ? 'info' : 'error'}`} role="status">
          <div>
            <strong>{result.outcome === 'delivered' ? 'Delivered' : result.outcome === 'accepted' ? 'Accepted' : result.outcome === 'rate_limited' ? 'Rate limited' : result.outcome === 'broken' ? 'Broken' : 'Error'}</strong>
            {result.message && <>: {result.message}</>}
            {result.outcome === 'rate_limited' && result.retryAfter !== undefined && <> Retry in {result.retryAfter} s.</>}
            {result.outcome === 'accepted' && kind === 'teams' && <p>{TEAMS_HINT}</p>}
          </div>
        </div>
      )}
      <div className="row">
        <button className="btn primary" disabled={save.busy || url.trim() === ''}>{save.busy ? 'Saving...' : 'Save'}</button>
        <button type="button" className="btn" disabled={!hook || test.busy} onClick={() => void onTest()}>{test.busy ? 'Sending...' : 'Send test'}</button>
        <button type="button" className="btn danger" disabled={!hook || remove.busy} onClick={() => void onRemove()}>Remove</button>
      </div>
    </form>
  )
}

function Webhooks() {
  const load = useCallback(() => api.getWebhooks(), [])
  const { data, error, loading, reload } = useLoad(load)
  return (
    <div className="stack">
      <h2>Expiry reminders</h2>
      <p className="muted">Get a message at 7, 5, 3, 2 and 1 days before a certificate expires, and daily once it has. Webhook URLs are never shown in full again.</p>
      {loading && <Spinner />}
      {error && <ErrorNote error={error} onRetry={reload} />}
      {data && (['discord', 'teams'] as const).map((k) => <WebhookCard key={k} kind={k} hook={data[k]} onChanged={reload} />)}
    </div>
  )
}

export function Account({ user }: { user: api.User }) {
  return (
    <section className="stack">
      <div className="page-head"><h1>Account</h1><span className="muted">{user.username}</span></div>
      <Appearance />
      <ChangePassword />
      <Webhooks />
    </section>
  )
}
