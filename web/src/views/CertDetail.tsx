import { useCallback, useState } from 'react'

import * as api from '../api'
import { ConfirmDialog, CopyButton, ErrorNote, fmtDate, Icon, Spinner, useAction, useLoad } from '../components/ui'
import { go, href } from '../nav'
import { DaysLeft } from './CertList'

const FILES: api.CertFile[] = ['crt', 'key', 'cnf']

export function CertDetail({ id }: { id: string }) {
  const load = useCallback(() => api.getCert(id), [id])
  const { data: c, error, loading, reload } = useLoad(load)
  const [tab, setTab] = useState<api.CertFile>('crt')
  const [deleting, setDeleting] = useState(false)
  const action = useAction()

  async function renew(cert: api.CertDetail) {
    const n = await action.run(() => api.renewCert(cert.id))
    if (n) go(href.cert(n.id))
  }

  if (loading) return <Spinner />
  if (error || !c) return <><a href={href.list}>&larr; Certificates</a><ErrorNote error={error ?? 'Not found'} onRetry={reload} /></>

  const content = c[tab]
  return (
    <section>
      <p><a href={href.list}>&larr; Certificates</a></p>
      <div className="page-head">
        <h1>{c.name} {c.renewedBy && <a className="badge" href={href.cert(c.renewedBy)}>renewed</a>}</h1>
        <div className="row">
          {!c.renewedBy && (
            <>
              <button type="button" className="btn" disabled={action.busy} onClick={() => void renew(c)}><Icon name="renew" /> Renew</button>
              <a className="btn" href={href.renew(c.id)}><Icon name="edit" /> Edit &amp; Renew</a>
            </>
          )}
          <button type="button" className="btn danger" onClick={() => setDeleting(true)}><Icon name="trash" /> Delete</button>
        </div>
      </div>
      {action.error && <ErrorNote error={action.error} />}

      <div className="card">
        <dl className="meta">
          <div><dt>Hosts</dt><dd>{c.hosts.join(', ')}</dd></div>
          <div><dt>Key size</dt><dd>{c.bits} bits</dd></div>
          <div><dt>Created</dt><dd>{fmtDate(c.createdAt)}</dd></div>
          <div><dt>Expires</dt><dd>{fmtDate(c.notAfter)} (<DaysLeft days={c.daysLeft} />)</dd></div>
        </dl>
      </div>

      <div className="card files">
        <div className="seg" role="tablist" aria-label="Files">
          {FILES.map((f) => (
            <button key={f} id={`tab-${f}`} type="button" role="tab" aria-selected={tab === f} aria-controls="file-panel" className={tab === f ? 'on' : ''} onClick={() => setTab(f)}>.{f}</button>
          ))}
        </div>
        <div className="row end">
          <CopyButton text={content} />
          <a className="btn small" href={api.fileUrl(c.id, tab)} download={`${c.name}.${tab}`}><Icon name="download" /> Download .{tab}</a>
        </div>
        {tab === 'key' && <p className="field-warn">Private key: keep it secret.</p>}
        <pre className="code" id="file-panel" role="tabpanel" aria-labelledby={`tab-${tab}`} tabIndex={0}>{content}</pre>
      </div>

      {deleting && (
        <ConfirmDialog
          title="Delete certificate"
          message={<>Delete <strong>{c.name}</strong> and its private key? This cannot be undone.</>}
          confirmLabel="Delete"
          onConfirm={async () => { await api.deleteCert(c.id); go(href.list) }}
          onClose={() => setDeleting(false)}
        />
      )}
    </section>
  )
}
