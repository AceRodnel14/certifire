// Per-user theme: mode (auto/light/dark) plus an optional accent hue that tints accents and
// backgrounds. Colours are set through the CSSOM (allowed by the CSP, unlike style attributes).
import type { Theme, ThemeMode } from './api'

export const DEFAULT_HUE = 24 // the flame orange of the logo
const CACHE_KEY = 'certifire.theme'
const MODES: ThemeMode[] = ['auto', 'light', 'dark']

export function normalizeTheme(t: Partial<Theme> | null | undefined): Theme {
  const mode = MODES.includes(t?.mode as ThemeMode) ? (t!.mode as ThemeMode) : 'auto'
  const h = t?.hue
  const hue = typeof h === 'number' && Number.isInteger(h) && h >= 0 && h < 360 ? h : null
  return { mode, hue }
}

const hsl = (h: number, s: number, l: number) => `hsl(${(h + 360) % 360} ${s}% ${l}%)`
/** Yellow/green hues are pulled darker so white text on the accent stays readable. */
const bump = (h: number) => Math.max(0, 1 - Math.abs(h - 100) / 75)

function hslToRgb(h: number, s: number, l: number): [number, number, number] {
  const sa = s / 100
  const li = l / 100
  const k = (n: number) => (n + h / 30) % 12
  const a = sa * Math.min(li, 1 - li)
  const f = (n: number) => li - a * Math.max(-1, Math.min(k(n) - 3, Math.min(9 - k(n), 1)))
  return [f(0), f(8), f(4)]
}

/** WCAG contrast of white text on hsl(h s l). */
export function contrastWithWhite(h: number, s: number, l: number): number {
  const lin = (c: number) => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4)
  const [r, g, b] = hslToRgb(((h % 360) + 360) % 360, s, l).map(lin) as [number, number, number]
  return 1.05 / (0.2126 * r + 0.7152 * g + 0.0722 * b + 0.05)
}

/** hsl() that keeps white text at AA (4.5:1) by darkening only as far as needed. */
export function fitWhite(h: number, s: number, l: number, min = 4.5): string {
  let li = l
  while (li > 10 && contrastWithWhite(h, s, li) < min) li -= 1
  return hsl(h, s, li)
}

export function themeVars(hue: number, dark: boolean): Record<string, string> {
  const b = bump(hue)
  const common = {
    '--accent-glow': hsl(hue - 5, 80, 52 - 14 * b),
    '--accent-hi': hsl(hue - 7, 87, 59 - 16 * b),
    '--accent-lo': hsl(hue + 3, 100, 20 - 4 * b),
  }
  return dark
    ? {
        ...common,
        '--accent': fitWhite(hue, 74, 52 - 17 * b),
        '--accent-strong': hsl(hue, 80, 62 - 12 * b),
        '--accent-soft': hsl(hue, 45, 16),
        '--bar-a': fitWhite(hue, 70, 38 - 8 * b),
        '--bar-b': fitWhite(hue - 8, 75, 44 - 10 * b),
        '--bg': hsl(hue, 28, 8),
        '--surface': hsl(hue, 24, 11),
        '--surface-2': hsl(hue, 24, 14),
        '--hover': hsl(hue, 26, 17),
        '--border': hsl(hue, 22, 20),
        '--border-strong': hsl(hue, 20, 29),
      }
    : {
        ...common,
        '--accent': fitWhite(hue, 90, 40 - 13 * b),
        '--accent-strong': fitWhite(hue, 92, 32 - 9 * b, 6),
        '--accent-soft': hsl(hue, 80, 94),
        '--bar-a': fitWhite(hue, 90, 38 - 12 * b),
        '--bar-b': fitWhite(hue - 8, 92, 44 - 14 * b),
        '--bg': hsl(hue, 42, 96),
        '--surface': hsl(hue, 40, 99),
        '--surface-2': hsl(hue, 38, 97),
        '--hover': hsl(hue, 45, 93),
        '--border': hsl(hue, 26, 88),
        '--border-strong': hsl(hue, 22, 78),
      }
}

const darkQuery = typeof matchMedia === 'function' ? matchMedia('(prefers-color-scheme: dark)') : null
let current: Theme = { mode: 'auto', hue: null }
const VAR_NAMES = Object.keys(themeVars(0, false))

/** The theme currently applied (what the UI shows). */
export const currentTheme = (): Theme => current

export function applyTheme(t: Theme): void {
  current = t
  if (typeof document === 'undefined') return
  const root = document.documentElement
  if (t.mode === 'auto') root.removeAttribute('data-theme')
  else root.setAttribute('data-theme', t.mode)
  for (const v of VAR_NAMES) root.style.removeProperty(v)
  if (t.hue === null) return
  const dark = t.mode === 'dark' || (t.mode === 'auto' && !!darkQuery?.matches)
  for (const [k, v] of Object.entries(themeVars(t.hue, dark))) root.style.setProperty(k, v)
}

// In auto mode the tints follow the OS light/dark setting.
darkQuery?.addEventListener?.('change', () => applyTheme(current))

export function cacheTheme(t: Theme): void {
  try {
    localStorage.setItem(CACHE_KEY, JSON.stringify(t))
  } catch {
    /* storage unavailable: the server copy still applies */
  }
}

/** Apply the cached theme at startup so the login screens do not flash the wrong colours. */
export function applyCachedTheme(): void {
  try {
    const raw = localStorage.getItem(CACHE_KEY)
    if (raw) applyTheme(normalizeTheme(JSON.parse(raw) as Partial<Theme>))
  } catch {
    /* ignore */
  }
}
