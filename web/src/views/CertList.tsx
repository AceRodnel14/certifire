import { useCallback, useState } from 'react'

import * as api from '../api'
import { ConfirmDialog, ErrorNote, fmtDate, Icon, Spinner, useAction, useLoad } from '../components/ui'
import { go, href } from '../nav'

export function DaysLeft({ days }: { days: number }) {
  const cls = days < 0 ? 'bad' : days <= 7 ? 'warn' : 'ok'
  const text = days < 0 ? `expired ${-days}d ago` : `${days} d`
  return <span className={`days ${cls}`}>{text}</span>
}

export function HostList({ hosts, max = 3 }: { hosts: string[]; max?: number }) {
  return (
    <span className="hosts" title={hosts.join(', ')}>
      {hosts.slice(0, max).join(', ')}
      {hosts.length > max && <span className="more"> +{hosts.length - max}</span>}
    </span>
  )
}

export function CertList() {
  const load = useCallback(() => api.listCerts(), [])
  const { data, error, loading, reload } = useLoad(load)
  const [deleting, setDeleting] = useState<api.CertSummary | null>(null)
  const action = useAction()

  async function renew(c: api.CertSummary) {
    const n = await action.run(() => api.renewCert(c.id))
    if (n) go(href.cert(n.id))
  }

  return (
    <section>
      <div className="page-head">
        <h1>Certificates</h1>
        <a className="btn primary" href={href.new}>
          <Icon name="plus" /> New certificate
        </a>
      </div>
      {action.error && <ErrorNote error={action.error} />}
      {loading && <Spinner />}
      {error && <ErrorNote error={error} onRetry={reload} />}
      {data && data.length === 0 && (
        <div className="card empty">
          <p>No certificates yet.</p>
          <a className="btn primary" href={href.new}>Create your first certificate</a>
        </div>
      )}
      {data && data.length > 0 && (
        <div className="card table-card">
          <table className="table">
            <thead>
              <tr>
                <th>Name</th>
                <th>Hosts</th>
                <th>Size</th>
                <th>Created</th>
                <th>Expires</th>
                <th>Days left</th>
                <th><span className="sr-only">Actions</span></th>
              </tr>
            </thead>
            <tbody>
              {data.map((c) => (
                <tr key={c.id}>
                  <td data-label="Name">
                    <a className="name-link" href={href.cert(c.id)}>{c.name}</a>
                    {c.renewedBy && (
                      <>
                        {' '}
                        <a className="badge" href={href.cert(c.renewedBy)} title="Open the newer certificate">renewed</a>
                      </>
                    )}
                  </td>
                  <td data-label="Hosts"><HostList hosts={c.hosts} /></td>
                  <td data-label="Size">{c.bits}</td>
                  <td data-label="Created">{fmtDate(c.createdAt)}</td>
                  <td data-label="Expires">{fmtDate(c.notAfter)}</td>
                  <td data-label="Days left"><DaysLeft days={c.daysLeft} /></td>
                  <td className="actions">
                    <a className="btn small" href={href.cert(c.id)}>Open</a>
                    {!c.renewedBy && (
                      <>
                        <button type="button" className="btn small" disabled={action.busy} onClick={() => void renew(c)} title="Same settings, new key">
                          <Icon name="renew" /> Renew
                        </button>
                        <a className="btn small" href={href.renew(c.id)} title="Change settings, then renew">
                          <Icon name="edit" /> Edit &amp; Renew
                        </a>
                      </>
                    )}
                    <button type="button" className="btn small danger" aria-label={`Delete ${c.name}`} onClick={() => setDeleting(c)}>
                      <Icon name="trash" />
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {deleting && (
        <ConfirmDialog
          title="Delete certificate"
          message={<>Delete <strong>{deleting.name}</strong> and its private key? This cannot be undone.</>}
          confirmLabel="Delete"
          onConfirm={async () => { await api.deleteCert(deleting.id); reload() }}
          onClose={() => setDeleting(null)}
        />
      )}
    </section>
  )
}
