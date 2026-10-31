import { useState, type FormEvent } from 'react'

import * as api from '../api'
import { ChipInput } from '../components/ChipInput'
import { InfoTip } from '../components/InfoTip'
import { ErrorNote, useAction } from '../components/ui'
import { go, href } from '../nav'
import { checkDays, checkName, checkSubjectField, cleanSubject, SUBJECT_KEYS } from '../validate'

const SUBJECT_LABELS: Record<api.SubjectKey, string> = {
  C: 'Country (C)',
  ST: 'State (ST)',
  L: 'Locality (L)',
  O: 'Organization (O)',
  OU: 'Org. unit (OU)',
}
const BITS = [2048, 3072, 4096]
const TIPS = {
  name: 'Short label for this certificate. Used as the file names (name.crt, name.key, name.cnf). Letters, digits, . _ -. e.g. homelab, k8s-ingress, nas',
  hosts: 'Every hostname or IP clients use to reach the app (the SAN list). The first one also becomes the CN. A wildcard covers one level only. e.g. app.example.lan, *.k8s.example.lan, 192.0.2.10',
  bits: 'Strength of the private key. 2048: fastest, works everywhere. 3072: middle ground. 4096: strongest, slightly slower TLS handshakes (default).',
  days: 'How long the certificate is valid. 825 is the most Apple devices accept. e.g. 365 (1 year), 825 (~2.25 years)',
  subject: 'Descriptive fields stored in the certificate. Browsers ignore them; leave empty if unsure.',
  C: 'Two-letter country code. e.g. US, DE, JP',
  ST: 'State or province. e.g. California, Bavaria',
  L: 'City. e.g. Austin, Munich',
  O: 'Organisation. e.g. Homelab, Example Inc.',
  OU: 'Department or team. e.g. IT, Infrastructure',
}

function FieldLabel({ htmlFor, tip, children }: { htmlFor: string; tip: string; children: string }) {
  return (
    <div className="label-row">
      <label className="field-label" htmlFor={htmlFor}>{children}</label>
      <InfoTip text={tip} label={`About ${children}`} />
    </div>
  )
}

/** Create form; with `renewOf` it is the Edit & Renew form, prefilled from that certificate. */
export function CertForm({ renewOf }: { renewOf?: api.CertDetail }) {
  const [name, setName] = useState(renewOf?.name ?? '')
  const [hosts, setHosts] = useState<string[]>(renewOf?.hosts ?? [])
  const [bits, setBits] = useState(renewOf?.bits ?? 4096)
  const [days, setDays] = useState(String(renewOf?.days ?? 825))
  const [subject, setSubject] = useState<Record<api.SubjectKey, string>>({
    C: renewOf?.subject?.C ?? '', ST: renewOf?.subject?.ST ?? '', L: renewOf?.subject?.L ?? '',
    O: renewOf?.subject?.O ?? '', OU: renewOf?.subject?.OU ?? '',
  })
  const [showErrors, setShowErrors] = useState(false)
  const { busy, error, run } = useAction()

  const nameErr = checkName(name)
  const daysErr = checkDays(Number(days))
  const hostsErr = hosts.length === 0 ? 'Add at least one host' : hosts[0].length > 64 ? 'The first host becomes the CN and must be at most 64 characters: reorder the hosts' : null
  const subjErr = Object.fromEntries(SUBJECT_KEYS.map((k) => [k, checkSubjectField(k, subject[k])])) as Record<api.SubjectKey, string | null>
  const valid = !nameErr && !daysErr && !hostsErr && SUBJECT_KEYS.every((k) => !subjErr[k])

  async function submit(e: FormEvent) {
    e.preventDefault()
    setShowErrors(true)
    if (!valid) return
    const input: api.CertInput = { name, hosts, bits, days: Number(days), subject: cleanSubject(subject) }
    if (renewOf) input.renewOf = renewOf.id
    const created = await run(() => api.createCert(input))
    if (created) go(href.cert(created.id))
  }

  return (
    <section>
      <div className="page-head">
        <h1>{renewOf ? `Renew ${renewOf.name}` : 'New certificate'}</h1>
      </div>
      <form className="card stack form" onSubmit={(e) => void submit(e)} noValidate>
        {renewOf && <p className="muted">A new key and certificate are generated with the values below; {renewOf.name} is then marked as renewed.</p>}
        <div className="field">
          <FieldLabel htmlFor="cert-name" tip={TIPS.name}>Name</FieldLabel>
          <input id="cert-name" className="input" value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" spellCheck={false} aria-invalid={showErrors && !!nameErr} />
          <span className="hint">Used for the download file names (name.crt, name.key, name.cnf).</span>
          {showErrors && nameErr && <span className="field-error">{nameErr}</span>}
        </div>

        <div className="field">
          <FieldLabel htmlFor="hosts" tip={TIPS.hosts}>Hosts</FieldLabel>
          <ChipInput id="hosts" value={hosts} onChange={setHosts} disabled={busy} />
          <span className="hint">DNS names, wildcards (*.k8s.example.test) or IP addresses. Press Enter to add. The first one is the CN.</span>
          {showErrors && hostsErr && <span className="field-error">{hostsErr}</span>}
        </div>

        <div className="grid2">
          <div className="field">
            <FieldLabel htmlFor="cert-bits" tip={TIPS.bits}>RSA key size</FieldLabel>
            <select id="cert-bits" className="input" value={bits} onChange={(e) => setBits(Number(e.target.value))}>
              {BITS.map((b) => <option key={b} value={b}>{b} bits</option>)}
            </select>
          </div>
          <div className="field">
            <FieldLabel htmlFor="cert-days" tip={TIPS.days}>Valid for (days)</FieldLabel>
            <input id="cert-days" className="input" type="number" min={1} max={825} step={1} inputMode="numeric" value={days} onChange={(e) => setDays(e.target.value)} aria-invalid={showErrors && !!daysErr} />
            {showErrors && daysErr && <span className="field-error">{daysErr}</span>}
          </div>
        </div>

        <fieldset className="subject">
          <legend>Subject (optional) <InfoTip text={TIPS.subject} label="About the subject" /></legend>
          <div className="grid5">
            {SUBJECT_KEYS.map((k) => (
              <div className="field" key={k}>
                <FieldLabel htmlFor={`subject-${k}`} tip={TIPS[k]}>{SUBJECT_LABELS[k]}</FieldLabel>
                <input id={`subject-${k}`} className="input" value={subject[k]} maxLength={k === 'C' ? 2 : 64} onChange={(e) => setSubject({ ...subject, [k]: e.target.value })} aria-invalid={!!subjErr[k]} autoComplete="off" />
                {subjErr[k] && <span className="field-error">{subjErr[k]}</span>}
              </div>
            ))}
          </div>
        </fieldset>

        {error && <ErrorNote error={error} />}
        <div className="row end">
          <a className="btn" href={renewOf ? href.cert(renewOf.id) : href.list}>Cancel</a>
          <button className="btn primary" disabled={busy}>
            {busy ? 'Generating...' : renewOf ? 'Generate renewal' : 'Generate certificate'}
          </button>
        </div>
        {busy && <p className="muted">Generating an RSA key can take a few seconds.</p>}
      </form>
    </section>
  )
}
