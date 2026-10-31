import { useCallback, useState, type FormEvent } from 'react'

import * as api from '../api'
import { InfoTip } from '../components/InfoTip'
import { ErrorNote, fmtDate, Modal, Spinner, useAction, useLoad } from '../components/ui'
import { checkPassword } from '../validate'

function ResetDialog({ user, onClose }: { user: api.AdminUser; onClose: () => void }) {
  const [masterKey, setMasterKey] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [done, setDone] = useState(false)
  const { busy, error, setError, run } = useAction()

  async function submit(e: FormEvent) {
    e.preventDefault()
    const bad = checkPassword(password, confirm)
    if (bad) return setError(bad)
    const r = await run(async () => { await api.resetUserPassword(user.id, masterKey, password, confirm); return true })
    if (r) setDone(true)
  }

  return (
    <Modal title={`Reset password for ${user.username}`} onClose={onClose}>
      {done ? (
        <>
          <div className="note ok" role="status">Password reset. {user.username}'s sessions were signed out.</div>
          <div className="row end"><button type="button" className="btn primary" onClick={onClose}>Done</button></div>
        </>
      ) : (
        <form className="stack" onSubmit={(e) => void submit(e)}>
          <div className="field">
            <div className="label-row">
              <label className="field-label" htmlFor="reset-master">Master key</label>
              <InfoTip text="The 20-character key shown once at first-run setup. It is needed to reset another user's password." label="About the master key" />
            </div>
            <input id="reset-master" className="input mono" type="password" autoComplete="off" spellCheck={false} value={masterKey} onChange={(e) => setMasterKey(e.target.value)} required />
          </div>
          <label className="field"><span className="field-label">New password</span>
            <input className="input" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} required /></label>
          <label className="field"><span className="field-label">Confirm new password</span>
            <input className="input" type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} required /></label>
          {error && <ErrorNote error={error} />}
          <div className="row end">
            <button type="button" className="btn" onClick={onClose} disabled={busy}>Cancel</button>
            <button className="btn primary" disabled={busy}>{busy ? 'Resetting...' : 'Reset password'}</button>
          </div>
        </form>
      )}
    </Modal>
  )
}

export function Admin() {
  const load = useCallback(() => api.listUsers(), [])
  const { data, error, loading, reload } = useLoad(load)
  const [target, setTarget] = useState<api.AdminUser | null>(null)
  return (
    <section>
      <div className="page-head"><h1>Users</h1></div>
      {loading && <Spinner />}
      {error && <ErrorNote error={error} onRetry={reload} />}
      {data && (
        <div className="card table-card">
          <table className="table">
            <thead><tr><th>Username</th><th>Role</th><th>Created</th><th>Certificates</th><th><span className="sr-only">Actions</span></th></tr></thead>
            <tbody>
              {data.map((u) => (
                <tr key={u.id}>
                  <td data-label="Username">{u.username}</td>
                  <td data-label="Role">{u.isAdmin ? <span className="badge">admin</span> : 'user'}</td>
                  <td data-label="Created">{fmtDate(u.createdAt)}</td>
                  <td data-label="Certificates">{u.certCount}</td>
                  <td className="actions"><button type="button" className="btn small" onClick={() => setTarget(u)}>Reset password</button></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {target && <ResetDialog user={target} onClose={() => setTarget(null)} />}
    </section>
  )
}
