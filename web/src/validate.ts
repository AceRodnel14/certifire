// Client-side mirror of the server's validation (spec section 5). The server stays
// authoritative; this only gives early, inline feedback.
import type { Subject, SubjectKey } from './api'

export const MAX_HOSTS = 100
export const NAME_RE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/
const LABEL_RE = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/
const OCTET_RE = /^(0|[1-9]\d{0,2})$/

export type HostCheck = { ok: true; host: string; warning?: string } | { ok: false; error: string }

export const WILDCARD_WARNING =
  'Chrome rejects wildcards with a single label after the *, such as *.homelab. It is accepted here, but use at least two labels (*.k8s.homelab) for Chrome.'

function isIPv4(s: string): boolean {
  const parts = s.split('.')
  return parts.length === 4 && parts.every((p) => OCTET_RE.test(p) && Number(p) <= 255)
}

function isIPv6(s: string): boolean {
  if (!s.includes(':') || !/^[0-9a-f:.]+$/.test(s)) return false
  try {
    new URL(`http://[${s}]/`)
    return true
  } catch {
    return false
  }
}

function labelsError(labels: string[]): string | null {
  for (const l of labels) {
    if (l === '') return 'Empty label (stray or doubled dot)'
    if (l.length > 63) return 'A label is longer than 63 characters'
    if (!LABEL_RE.test(l)) return `Invalid label "${l}": use letters, digits and hyphens, not starting or ending with a hyphen`
  }
  return null
}

/** Check one host entry: IP, DNS name or wildcard. Entries are trimmed and lowercased. */
export function checkHost(input: string): HostCheck {
  const host = input.trim().toLowerCase()
  if (host === '') return { ok: false, error: 'Enter a host name or IP address' }
  if (host.includes('%')) return { ok: false, error: 'IPv6 zones are not allowed' }
  if (isIPv4(host)) return { ok: true, host }
  if (isIPv6(host)) return { ok: true, host: new URL(`http://[${host}]/`).hostname.slice(1, -1) }
  if (host.length > 253) return { ok: false, error: 'Longer than 253 characters' }
  if (host === '*') return { ok: false, error: 'A bare * is not allowed; use *.example.test' }
  if (host.includes('*')) {
    if (!host.startsWith('*.')) return { ok: false, error: '* must be the whole first label (*.example.test)' }
    const rest = host.slice(2).split('.')
    if (rest.some((l) => l.includes('*'))) return { ok: false, error: '* is only allowed as the first label' }
    const err = labelsError(rest)
    if (err) return { ok: false, error: err }
    return rest.length === 1 ? { ok: true, host, warning: WILDCARD_WARNING } : { ok: true, host }
  }
  const err = labelsError(host.split('.'))
  if (err) return { ok: false, error: err }
  return { ok: true, host }
}

/** True for `*.<single-label>` entries. */
export const isSingleLabelWildcard = (host: string): boolean => /^\*\.[^.*]+$/.test(host)

export type AddResult = { hosts: string[]; rest: string; error: string | null }

/**
 * Add the entries typed or pasted into the chip input. Entries split on whitespace and commas.
 * Valid ones are appended (deduplicated); invalid ones stay in `rest` with an error.
 */
export function addTokens(hosts: string[], text: string): AddResult {
  const out = [...hosts]
  const bad: string[] = []
  let error: string | null = null
  for (const token of text.split(/[\s,]+/).filter(Boolean)) {
    const c = checkHost(token)
    if (!c.ok) {
      bad.push(token)
      error ??= c.error
    } else if (!out.includes(c.host)) {
      if (out.length >= MAX_HOSTS) {
        bad.push(token)
        error ??= `At most ${MAX_HOSTS} hosts`
      } else out.push(c.host)
    }
  }
  return { hosts: out, rest: bad.join(' '), error }
}

export function checkName(name: string): string | null {
  if (name === '') return 'Name is required'
  if (!NAME_RE.test(name)) return 'Use letters, digits, dot, underscore and hyphen (max 64), starting with a letter or digit'
  return null
}

export function checkDays(days: number): string | null {
  return Number.isInteger(days) && days >= 1 && days <= 825 ? null : 'Days must be a whole number from 1 to 825'
}

const SUBJECT_RE = /^[A-Za-z0-9 .,'()&/+-]+$/
export const SUBJECT_KEYS: SubjectKey[] = ['C', 'ST', 'L', 'O', 'OU']

export function checkSubjectField(key: SubjectKey, raw: string): string | null {
  const v = raw.trim()
  if (v === '') return null
  if (key === 'C') return /^[A-Za-z]{2}$/.test(v) ? null : 'Two letters, e.g. DE'
  if (v.length > 64) return 'At most 64 characters'
  return SUBJECT_RE.test(v) ? null : "Allowed: letters, digits, space and . , ' ( ) & / + -"
}

/** Trim, uppercase C, drop empty keys. */
export function cleanSubject(s: Subject): Subject {
  const out: Subject = {}
  for (const k of SUBJECT_KEYS) {
    const v = (s[k] ?? '').trim()
    if (v) out[k] = k === 'C' ? v.toUpperCase() : v
  }
  return out
}

export function checkPassword(password: string, confirm: string): string | null {
  if (password.length < 8) return 'Password must be at least 8 characters'
  if (password !== confirm) return 'Passwords do not match'
  return null
}

/** Same rule as the server: floor((notAfter - now) / 24h). */
export function daysLeft(notAfter: string, now = Date.now()): number {
  return Math.floor((Date.parse(notAfter) - now) / 86_400_000)
}
