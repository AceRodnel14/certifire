// Tiny hash navigation (no router library): #/ list, #/new, #/cert/<id>, #/renew/<id>, #/account, #/admin.
import { useEffect, useState } from 'react'

export type Route =
  | { name: 'list' }
  | { name: 'new' }
  | { name: 'cert'; id: string }
  | { name: 'renew'; id: string }
  | { name: 'account' }
  | { name: 'admin' }

export function parseRoute(hash: string): Route {
  const parts = hash.replace(/^#\/?/, '').split('/').filter(Boolean)
  const [a, b] = parts
  if (a === 'new' && parts.length === 1) return { name: 'new' }
  if (a === 'cert' && b && parts.length === 2) return { name: 'cert', id: decodeURIComponent(b) }
  if (a === 'renew' && b && parts.length === 2) return { name: 'renew', id: decodeURIComponent(b) }
  if (a === 'account' && parts.length === 1) return { name: 'account' }
  if (a === 'admin' && parts.length === 1) return { name: 'admin' }
  return { name: 'list' }
}

export const href = {
  list: '#/',
  new: '#/new',
  cert: (id: string) => `#/cert/${encodeURIComponent(id)}`,
  renew: (id: string) => `#/renew/${encodeURIComponent(id)}`,
  account: '#/account',
  admin: '#/admin',
}

export const go = (h: string): void => {
  window.location.hash = h
}

export function useRoute(): Route {
  const [hash, setHash] = useState(window.location.hash)
  useEffect(() => {
    const on = () => setHash(window.location.hash)
    window.addEventListener('hashchange', on)
    return () => window.removeEventListener('hashchange', on)
  }, [])
  return parseRoute(hash)
}
