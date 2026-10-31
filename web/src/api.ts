// The one typed client for the CertiFire API (spec section 4). Same origin in production
// (embedded UI), proxied to the Go server by `npm run dev`.

export type ThemeMode = 'auto' | 'light' | 'dark'
export type Theme = { mode: ThemeMode; hue: number | null }
export type User = { username: string; isAdmin: boolean; theme: Theme }
export type AppState = { setupDone: boolean; user: User | null }

export type Subject = { C?: string; ST?: string; L?: string; O?: string; OU?: string }
export type SubjectKey = keyof Subject

export type CertSummary = {
  id: string
  name: string
  hosts: string[]
  bits: number
  days: number
  createdAt: string
  notAfter: string
  daysLeft: number
  renewedBy: string | null
}
// GET /api/certs/{id}: list fields plus subject ({} when empty), notBefore and the three files.
export type CertDetail = CertSummary & {
  subject: Subject
  notBefore: string
  crt: string
  key: string
  cnf: string
}
export type CertInput = {
  name: string
  hosts: string[]
  bits: number
  days: number
  subject: Subject
  renewOf?: string
}

export type WebhookKind = 'discord' | 'teams'
export type Webhook = {
  masked: string
  broken: boolean
  brokenReason: string | null
  brokenAt: string | null
  updatedAt: string
}
export type Webhooks = { discord: Webhook | null; teams: Webhook | null }
export type TestOutcome = {
  outcome: 'delivered' | 'accepted' | 'broken' | 'rate_limited' | 'error'
  message: string
  retryAfter?: number
}

export type AdminUser = {
  id: number
  username: string
  isAdmin: boolean
  createdAt: string
  certCount: number
}

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

let onUnauthorized: (() => void) | undefined
/** Called when a request that needs a session gets 401 (expired or revoked session). */
export function setUnauthorizedHandler(fn: (() => void) | undefined): void {
  onUnauthorized = fn
}

type Opts = { body?: unknown; keepOn401?: boolean }

async function request<T>(method: string, path: string, opts: Opts = {}): Promise<T> {
  const init: RequestInit = { method, credentials: 'same-origin', headers: { Accept: 'application/json' } }
  if (opts.body !== undefined) {
    init.headers = { ...init.headers, 'Content-Type': 'application/json' }
    init.body = JSON.stringify(opts.body)
  }
  let res: Response
  try {
    res = await fetch(path, init)
  } catch {
    throw new ApiError(0, 'Cannot reach the server')
  }
  if (!res.ok) {
    let message = `Request failed (${res.status})`
    try {
      const data = (await res.json()) as { error?: unknown }
      if (typeof data.error === 'string' && data.error) message = data.error
    } catch {
      /* non-JSON error body: keep the generic message */
    }
    if (res.status === 401 && !opts.keepOn401) onUnauthorized?.()
    throw new ApiError(res.status, message)
  }
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

const enc = encodeURIComponent

export const getState = () => request<AppState>('GET', '/api/state')
export const setup = (password: string, confirm: string) =>
  request<{ masterKey: string }>('POST', '/api/setup', { body: { password, confirm }, keepOn401: true })
export const register = (username: string, password: string, confirm: string) =>
  request<void>('POST', '/api/register', { body: { username, password, confirm }, keepOn401: true })
export const login = (username: string, password: string) =>
  request<void>('POST', '/api/login', { body: { username, password }, keepOn401: true })
export const logout = () => request<void>('POST', '/api/logout', { keepOn401: true })

export const changePassword = (currentPassword: string, password: string, confirm: string) =>
  request<void>('POST', '/api/account/password', { body: { currentPassword, password, confirm }, keepOn401: true })
export const saveTheme = (theme: Theme) => request<void>('PUT', '/api/account/theme', { body: theme })

export const getWebhooks = () => request<Webhooks>('GET', '/api/account/webhooks')
export const saveWebhook = (kind: WebhookKind, url: string) =>
  request<Webhook>('PUT', `/api/account/webhooks/${kind}`, { body: { url } })
export const deleteWebhook = (kind: WebhookKind) => request<void>('DELETE', `/api/account/webhooks/${kind}`)
export const testWebhook = (kind: WebhookKind) =>
  request<TestOutcome>('POST', `/api/account/webhooks/${kind}/test`)

export const listCerts = () => request<CertSummary[]>('GET', '/api/certs')
export const getCert = (id: string) => request<CertDetail>('GET', `/api/certs/${enc(id)}`)
export const createCert = (input: CertInput) => request<CertSummary>('POST', '/api/certs', { body: input })
export const renewCert = (id: string) => request<CertSummary>('POST', `/api/certs/${enc(id)}/renew`)
export const deleteCert = (id: string) => request<void>('DELETE', `/api/certs/${enc(id)}`)
export type CertFile = 'crt' | 'key' | 'cnf'
export const fileUrl = (id: string, file: CertFile) => `/api/certs/${enc(id)}/files/${file}`

export const listUsers = () => request<AdminUser[]>('GET', '/api/admin/users')
export const resetUserPassword = (id: number, masterKey: string, password: string, confirm: string) =>
  request<void>('POST', `/api/admin/users/${id}/password`, { body: { masterKey, password, confirm }, keepOn401: true })
