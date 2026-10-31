import { describe, expect, test } from 'vitest'

import { addTokens, checkDays, checkHost, checkName, checkPassword, checkSubjectField, cleanSubject, daysLeft, isSingleLabelWildcard } from './validate'

describe('checkHost', () => {
  test.each([
    ['app.example.com', 'app.example.com'],
    ['  APP.Example.COM ', 'app.example.com'],
    ['localhost', 'localhost'],
    ['*.k8s.example.test', '*.k8s.example.test'],
    ['192.0.2.10', '192.0.2.10'],
    ['2001:db8::1', '2001:db8::1'],
    ['a-b.c1.test', 'a-b.c1.test'],
  ])('accepts %s', (input, host) => {
    expect(checkHost(input)).toMatchObject({ ok: true, host })
  })

  test.each([
    '', 'a.*.b', '*foo.b', '**.b', '*', 'foo_bar.test', 'a b.test', '-a.test', 'a-.test', 'a..test', '.a.test',
    'münchen.test', 'fe80::1%eth0', 'x'.repeat(64) + '.test', ('a.'.repeat(130)) + 'com', '*.', '*.a.*',
    'a;b', '$(id)', '../etc', 'a/b',
  ])('rejects %j', (input) => {
    expect(checkHost(input).ok).toBe(false)
  })

  test('single-label wildcard is accepted with a Chrome warning', () => {
    const r = checkHost('*.homelab')
    expect(r).toMatchObject({ ok: true, host: '*.homelab' })
    expect(r.ok && r.warning).toMatch(/Chrome/)
    expect(isSingleLabelWildcard('*.homelab')).toBe(true)
  })

  test('two-label wildcard has no warning', () => {
    const r = checkHost('*.k8s.homelab')
    expect(r.ok && r.warning).toBeUndefined()
    expect(isSingleLabelWildcard('*.k8s.homelab')).toBe(false)
  })

  test('numeric-looking names that are not IPs follow the DNS rules', () => {
    expect(checkHost('999.1.1.1').ok).toBe(true)
    expect(checkHost('01.2.3.4')).toMatchObject({ ok: true })
  })
})

describe('addTokens', () => {
  test('adds valid, lowercases and dedupes', () => {
    expect(addTokens(['a.test'], 'A.test b.test')).toEqual({ hosts: ['a.test', 'b.test'], rest: '', error: null })
  })
  test('splits pasted lists on commas, spaces and newlines', () => {
    expect(addTokens([], 'a.test,b.test\nc.test').hosts).toEqual(['a.test', 'b.test', 'c.test'])
  })
  test('keeps invalid entries in the input with an error', () => {
    const r = addTokens([], 'good.test bad_host')
    expect(r.hosts).toEqual(['good.test'])
    expect(r.rest).toBe('bad_host')
    expect(r.error).toMatch(/Invalid label/)
  })
  test('caps at 100 hosts', () => {
    const many = Array.from({ length: 100 }, (_, i) => `h${i}.test`)
    const r = addTokens(many, 'extra.test')
    expect(r.hosts).toHaveLength(100)
    expect(r.rest).toBe('extra.test')
    expect(r.error).toMatch(/100/)
  })
})

describe('form fields', () => {
  test('name', () => {
    expect(checkName('my-cert_1.v2')).toBeNull()
    for (const n of ['', '.hidden', 'a/b', 'a b', '-x', 'x'.repeat(65)]) expect(checkName(n)).not.toBeNull()
  })
  test('days', () => {
    expect(checkDays(1)).toBeNull()
    expect(checkDays(825)).toBeNull()
    for (const d of [0, 826, 1.5, NaN]) expect(checkDays(d)).not.toBeNull()
  })
  test('subject', () => {
    expect(checkSubjectField('C', 'de')).toBeNull()
    expect(checkSubjectField('C', 'DEU')).not.toBeNull()
    expect(checkSubjectField('O', "Home Lab & Co. (EU)")).toBeNull()
    for (const v of ['a$b', 'a#b', 'a\\b', 'a"b', 'a;b', 'a=b', 'x'.repeat(65)]) expect(checkSubjectField('O', v)).not.toBeNull()
    expect(checkSubjectField('OU', '')).toBeNull()
  })
  test('cleanSubject trims, uppercases C and drops empties', () => {
    expect(cleanSubject({ C: ' de ', ST: '', L: ' Berlin ', O: '  ' })).toEqual({ C: 'DE', L: 'Berlin' })
  })
  test('password', () => {
    expect(checkPassword('short', 'short')).toMatch(/8/)
    expect(checkPassword('longenough', 'different')).toMatch(/match/)
    expect(checkPassword('longenough', 'longenough')).toBeNull()
  })
  test('daysLeft floors like the server', () => {
    const now = Date.parse('2026-01-01T00:00:00Z')
    expect(daysLeft('2026-01-08T12:00:00Z', now)).toBe(7)
    expect(daysLeft('2025-12-31T12:00:00Z', now)).toBe(-1)
  })
})

test('IPv6 entries are normalised to the short form without brackets', () => {
  expect(checkHost('2001:0DB8:0:0:0:0:0:1')).toMatchObject({ ok: true, host: '2001:db8::1' })
  expect(addTokens([], '2001:db8::1 2001:db8:0:0::1').hosts).toEqual(['2001:db8::1'])
})
