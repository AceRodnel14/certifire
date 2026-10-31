import { expect, test } from 'vitest'

import { tipOpen } from './tip'

test('hover, focus and click/tap open the tip (a tap is mouseenter then click: stays open)', () => {
  for (const ev of ['enter', 'focus', 'click'] as const) expect(tipOpen(ev)).toBe(true)
})
test('leave, blur, Escape and outside press close it', () => {
  for (const ev of ['leave', 'blur', 'escape', 'outside'] as const) expect(tipOpen(ev)).toBe(false)
})
