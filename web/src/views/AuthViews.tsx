import { useState, type FormEvent } from 'react'

import * as api from '../api'
import { Logo } from '../components/Logo'
import { CopyButton, ErrorNote, useAction } from '../components/ui'
import { checkPassword } from '../validate'

function Brand({ subtitle }: { subtitle: string }) {
  return (
    <div className="brand-block">
      <Logo height={96} />
      <h1>CertiFire</h1>
      <p className="muted">{subtitle}</p>
    </div>
  )
}

export function SetupView({ onDone }: { onDone: () => void }) {
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [masterKey, setMasterKey] = useState<string | null>(null)
  const { busy, error, setError, run } = useAction()

  async function submit(e: FormEvent) {
    e.preventDefault()
    const bad = checkPassword(password, confirm)
    if (bad) return setError(bad)
    const r = await run(() => api.setup(password, confirm))
    if (r) setMasterKey(r.masterKey)
  }

  if (masterKey) {
    return (
      <main className="auth">
        <div className="card auth-card">
          <Brand subtitle="Save your master key" />
          <p>
            This key resets forgotten passwords. It is shown <strong>only once</strong>: store it somewhere safe (a password manager) before continuing.
          </p>
          <pre className="master-key" aria-label="Master key">{masterKey}</pre>
          <div className="row">
            <CopyButton text={masterKey} label="Copy master key" />
            <button type="button" className="btn primary" onClick={onDone}>
              I saved it
            </button>
          </div>
        </div>
      </main>
    )
  }

  return (
    <main className="auth">
      <form className="card auth-card stack" onSubmit={(e) => void submit(e)}>
        <Brand subtitle="First-run setup: choose the password for the admin account" />
        <label className="field">
          <span className="field-label">Admin password</span>
          <input className="input" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
        </label>
        <label className="field">
          <span className="field-label">Confirm password</span>
          <input className="input" type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} required />
        </label>
        {error && <ErrorNote error={error} />}
        <button className="btn primary block" disabled={busy}>
          {busy ? 'Creating...' : 'Create admin account'}
        </button>
      </form>
    </main>
  )
}

export function AuthView({ onDone }: { onDone: () => void }) {
  const [tab, setTab] = useState<'login' | 'register'>('login')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const { busy, error, setError, run } = useAction()

  async function submit(e: FormEvent) {
    e.preventDefault()
    if (tab === 'register') {
      const bad = checkPassword(password, confirm)
      if (bad) return setError(bad)
    }
    const r = await run(async () => {
      if (tab === 'login') await api.login(username, password)
      else await api.register(username, password, confirm)
      return true
    })
    if (r) onDone()
  }

  return (
    <main className="auth">
      <form className="card auth-card stack" onSubmit={(e) => void submit(e)}>
        <Brand subtitle="Self-signed certificates for your homelab" />
        <div className="seg">
          {(['login', 'register'] as const).map((t) => (
            <button key={t} type="button" aria-pressed={tab === t} className={tab === t ? 'on' : ''} onClick={() => { setTab(t); setError(null) }}>
              {t === 'login' ? 'Log in' : 'Register'}
            </button>
          ))}
        </div>
        <label className="field">
          <span className="field-label">Username</span>
          <input className="input" autoComplete="username" autoCapitalize="none" value={username} onChange={(e) => setUsername(e.target.value)} required />
        </label>
        <label className="field">
          <span className="field-label">Password</span>
          <input className="input" type="password" autoComplete={tab === 'login' ? 'current-password' : 'new-password'} value={password} onChange={(e) => setPassword(e.target.value)} required />
        </label>
        {tab === 'register' && (
          <label className="field">
            <span className="field-label">Confirm password</span>
            <input className="input" type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} required />
            <span className="hint">3-32 characters for the username (letters, digits, . _ -); password at least 8.</span>
          </label>
        )}
        {error && <ErrorNote error={error} />}
        <button className="btn primary block" disabled={busy}>
          {busy ? 'Please wait...' : tab === 'login' ? 'Log in' : 'Create account'}
        </button>
      </form>
    </main>
  )
}
