import { afterEach, describe, expect, test, vi } from 'vitest'

import * as api from './api'

function mockFetch(res: Response | Error) {
  const f = vi.fn(async () => {
    if (res instanceof Error) throw res
    return res
  })
  vi.stubGlobal('fetch', f)
  return f
}
const lastCall = (f: ReturnType<typeof mockFetch>) => {
  const [url, init] = f.mock.calls[0] as unknown as [string, RequestInit]
  return { url, init }
}

afterEach(() => {
  vi.unstubAllGlobals()
  api.setUnauthorizedHandler(undefined)
})

describe('api client', () => {
  test('getState parses the state', async () => {
    const body = { setupDone: true, user: { username: 'admin', isAdmin: true, theme: { mode: 'auto', hue: null } } }
    const f = mockFetch(Response.json(body))
    await expect(api.getState()).resolves.toEqual(body)
    expect(lastCall(f).url).toBe('/api/state')
    expect(lastCall(f).init.method).toBe('GET')
  })

  test('sends JSON bodies with the right method and path', async () => {
    const f = mockFetch(Response.json({ masked: 'https://discord.com/…abcd', broken: false, brokenReason: null, brokenAt: null, updatedAt: 'x' }))
    await api.saveWebhook('discord', 'https://example.test/hook')
    const { url, init } = lastCall(f)
    expect(url).toBe('/api/account/webhooks/discord')
    expect(init.method).toBe('PUT')
    expect(init.body).toBe(JSON.stringify({ url: 'https://example.test/hook' }))
    expect(new Headers(init.headers).get('Content-Type')).toBe('application/json')
  })

  test('createCert posts the renewOf id', async () => {
    const f = mockFetch(Response.json({ id: 'n' }, { status: 201 }))
    await api.createCert({ name: 'a', hosts: ['a.test'], bits: 2048, days: 10, subject: {}, renewOf: 'old' })
    const { url, init } = lastCall(f)
    expect(url).toBe('/api/certs')
    expect(JSON.parse(init.body as string)).toMatchObject({ renewOf: 'old', bits: 2048 })
  })

  test('204 resolves to undefined', async () => {
    mockFetch(new Response(null, { status: 204 }))
    await expect(api.logout()).resolves.toBeUndefined()
  })

  test('maps {"error"} bodies to ApiError with the status', async () => {
    mockFetch(Response.json({ error: 'name taken' }, { status: 409 }))
    const err = await api.register('bob', 'password1', 'password1').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(api.ApiError)
    expect((err as api.ApiError).status).toBe(409)
    expect((err as api.ApiError).message).toBe('name taken')
  })

  test('falls back to a generic message for non-JSON errors', async () => {
    mockFetch(new Response('<html>', { status: 502 }))
    await expect(api.listCerts()).rejects.toThrow('Request failed (502)')
  })

  test('network failure becomes ApiError status 0', async () => {
    mockFetch(new TypeError('failed'))
    await expect(api.listCerts()).rejects.toMatchObject({ status: 0 })
  })

  test('401 calls the unauthorized handler, except for login-style calls', async () => {
    const handler = vi.fn()
    api.setUnauthorizedHandler(handler)
    mockFetch(Response.json({ error: 'expired' }, { status: 401 }))
    await expect(api.listCerts()).rejects.toThrow('expired')
    expect(handler).toHaveBeenCalledTimes(1)
    mockFetch(Response.json({ error: 'wrong password' }, { status: 401 }))
    await expect(api.login('a', 'b')).rejects.toThrow('wrong password')
    await expect(api.changePassword('a', 'b', 'b')).rejects.toThrow()
    expect(handler).toHaveBeenCalledTimes(1)
  })

  test('ids are URL-encoded and file URLs match the contract', () => {
    expect(api.fileUrl('a/b', 'key')).toBe('/api/certs/a%2Fb/files/key')
  })

  test('admin reset and test endpoints', async () => {
    const f = mockFetch(new Response(null, { status: 204 }))
    await api.resetUserPassword(7, 'KEY', 'newpassword', 'newpassword')
    expect(lastCall(f).url).toBe('/api/admin/users/7/password')
    mockFetch(Response.json({ outcome: 'accepted', message: 'ok' }))
    await expect(api.testWebhook('teams')).resolves.toMatchObject({ outcome: 'accepted' })
  })
})
