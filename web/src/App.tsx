import { useCallback, useEffect, useState } from 'react'

import * as api from './api'
import { Logo } from './components/Logo'
import { ErrorNote, Icon, Spinner, errMsg } from './components/ui'
import { go, href, useRoute } from './nav'
import { applyTheme, cacheTheme, normalizeTheme } from './theme'
import { Account } from './views/Account'
import { Admin } from './views/Admin'
import { AuthView, SetupView } from './views/AuthViews'
import { CertDetail } from './views/CertDetail'
import { CertForm } from './views/CertForm'
import { CertList } from './views/CertList'

function RenewForm({ id }: { id: string }) {
  const [res, setRes] = useState<{ id: string; cert?: api.CertDetail; error?: string } | null>(null)
  useEffect(() => {
    let live = true
    api.getCert(id).then((cert) => live && setRes({ id, cert }), (e: unknown) => live && setRes({ id, error: errMsg(e) }))
    return () => { live = false }
  }, [id])
  if (!res || res.id !== id) return <Spinner />
  if (res.error || !res.cert) return <ErrorNote error={res.error ?? 'Not found'} />
  return <CertForm renewOf={res.cert} />
}

function Shell({ user, onLogout }: { user: api.User; onLogout: () => void }) {
  const route = useRoute()
  const isAdminRoute = route.name === 'admin' && !user.isAdmin
  return (
    <>
      <header className="topbar">
        <a className="brand" href={href.list}>
          <Logo height={30} /> CertiFire
        </a>
        <nav>
          <a href={href.list} aria-current={route.name === 'list' ? 'page' : undefined}>Certificates</a>
          {user.isAdmin && <a href={href.admin} aria-current={route.name === 'admin' ? 'page' : undefined}>Users</a>}
          <a href={href.account} aria-current={route.name === 'account' ? 'page' : undefined}><Icon name="user" /> {user.username}</a>
          <button type="button" className="btn small ghost" onClick={onLogout}><Icon name="logout" /> Log out</button>
        </nav>
      </header>
      <main className="page">
        {route.name === 'list' && <CertList />}
        {route.name === 'new' && <CertForm />}
        {route.name === 'cert' && <CertDetail key={route.id} id={route.id} />}
        {route.name === 'renew' && <RenewForm id={route.id} />}
        {route.name === 'account' && <Account user={user} />}
        {route.name === 'admin' && (isAdminRoute ? <ErrorNote error="Admins only" /> : <Admin />)}
      </main>
    </>
  )
}

export function App() {
  const [state, setState] = useState<api.AppState | null>(null)
  const [error, setError] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    try {
      const s = await api.getState()
      if (s.user) {
        const theme = normalizeTheme(s.user.theme)
        applyTheme(theme)
        cacheTheme(theme)
      }
      setError(null)
      setState(s)
    } catch (e) {
      setError(errMsg(e))
    }
  }, [])

  useEffect(() => {
    api.setUnauthorizedHandler(() => void refresh())
    // eslint-disable-next-line react-hooks/set-state-in-effect -- initial load of server state
    void refresh()
    return () => api.setUnauthorizedHandler(undefined)
  }, [refresh])

  async function logout() {
    try { await api.logout() } catch { /* session may already be gone */ }
    go(href.list)
    await refresh()
  }

  if (error && !state) return <main className="auth"><div className="card auth-card"><ErrorNote error={error} onRetry={() => void refresh()} /></div></main>
  if (!state) return <main className="auth"><Spinner /></main>
  if (!state.setupDone) return <SetupView onDone={() => void refresh()} />
  if (!state.user) return <AuthView onDone={() => void refresh()} />
  return <Shell user={state.user} onLogout={() => void logout()} />
}
