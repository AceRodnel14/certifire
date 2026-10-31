import { useId } from 'react'

/** CertiFire logo. The lock uses currentColor (follows the theme); the flame keeps its gradient. */
export function Logo({ height = 32 }: { height?: number }) {
  const uid = useId().replace(/[^a-zA-Z0-9]/g, '')
  const grad = `cf-flame-${uid}`
  const mask = `cf-keyhole-${uid}`
  return (
    <svg className="logo" viewBox="70 50 650 830" height={height} width={(height * 650) / 830} aria-hidden="true" focusable="false">
      <defs>
        <linearGradient id={grad} x1="0.7" y1="0" x2="0.2" y2="1">
          <stop offset="0" stopColor="#FFC400" />
          <stop offset="0.5" stopColor="#F57C00" />
          <stop offset="1" stopColor="#E53920" />
        </linearGradient>
        <mask id={mask}>
          <rect x="0" y="0" width="816" height="976" fill="#fff" />
          <circle cx="392" cy="556" r="75" fill="#000" />
          <path d="M366 596 H418 L450 730 H333 Z" fill="#000" />
        </mask>
      </defs>
      <path fill={`url(#${grad})`} d="M518 62 C562 104 582 166 552 268 L618 208 C642 272 656 330 662 383 L628 383 L604 332 C568 382 516 406 450 408 C498 378 518 330 506 282 C496 246 472 226 440 212 C482 190 522 148 518 62 Z" />
      <path fill="currentColor" d="M163 383 V330 A227 222 0 0 1 470 125 L415 188 A150 146 0 0 0 240 330 V383 Z" />
      <path fill="currentColor" mask={`url(#${mask})`} d="M104 408 H680 Q702 408 702 430 V700 H548 Q528 700 525 720 L503 865 H104 Q82 865 82 843 V430 Q82 408 104 408 Z" />
      <path fill="currentColor" d="M566 728 H700 L540 862 L552 742 Q554 728 566 728 Z" />
    </svg>
  )
}
