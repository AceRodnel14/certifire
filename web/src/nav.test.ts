import { expect, test } from 'vitest'

import { parseRoute } from './nav'

test('parseRoute', () => {
  expect(parseRoute('')).toEqual({ name: 'list' })
  expect(parseRoute('#/new')).toEqual({ name: 'new' })
  expect(parseRoute('#/cert/abc')).toEqual({ name: 'cert', id: 'abc' })
  expect(parseRoute('#/renew/a%2Fb')).toEqual({ name: 'renew', id: 'a/b' })
  expect(parseRoute('#/account')).toEqual({ name: 'account' })
  expect(parseRoute('#/admin')).toEqual({ name: 'admin' })
  expect(parseRoute('#/cert')).toEqual({ name: 'list' })
  expect(parseRoute('#/nope/x')).toEqual({ name: 'list' })
})
