import { useRef, useState, type ClipboardEvent, type KeyboardEvent } from 'react'

import { addTokens, isSingleLabelWildcard, WILDCARD_WARNING } from '../validate'

type Props = {
  id: string
  value: string[]
  onChange: (hosts: string[]) => void
  disabled?: boolean
  describedBy?: string
}

/** Tag input for hosts: Enter, comma or paste adds; x or Backspace on empty input removes. */
export function ChipInput({ id, value, onChange, disabled, describedBy }: Props) {
  const [text, setText] = useState('')
  const [error, setError] = useState<string | null>(null)
  const inputRef = useRef<HTMLInputElement>(null)

  function commit(raw: string) {
    if (raw.trim() === '') {
      setText('')
      setError(null)
      return
    }
    const r = addTokens(value, raw)
    if (r.hosts.length !== value.length) onChange(r.hosts)
    setText(r.rest)
    setError(r.error)
  }

  function onKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === 'Enter' || e.key === ',') {
      e.preventDefault() // Enter must not submit the form
      commit(text)
    } else if (e.key === 'Backspace' && text === '' && value.length > 0) {
      onChange(value.slice(0, -1))
      setError(null)
    }
  }

  function onPaste(e: ClipboardEvent<HTMLInputElement>) {
    const pasted = e.clipboardData.getData('text')
    if (/[\s,]/.test(pasted.trim())) {
      e.preventDefault()
      commit(`${text} ${pasted}`)
    }
  }

  const warned = value.some(isSingleLabelWildcard)
  const errId = `${id}-error`

  return (
    <div>
      <div className={`chips input${error ? ' invalid' : ''}`} onClick={() => inputRef.current?.focus()}>
        {value.map((h) => (
          <span key={h} className={`chip${isSingleLabelWildcard(h) ? ' warn' : ''}`} title={isSingleLabelWildcard(h) ? WILDCARD_WARNING : undefined}>
            {h}
            <button type="button" aria-label={`Remove ${h}`} disabled={disabled} onClick={() => onChange(value.filter((x) => x !== h))}>
              ×
            </button>
          </span>
        ))}
        <input
          ref={inputRef}
          id={id}
          value={text}
          disabled={disabled}
          autoComplete="off"
          spellCheck={false}
          placeholder={value.length ? '' : 'app.example.com, *.k8s.example.test, 192.0.2.10'}
          aria-invalid={error ? true : undefined}
          aria-describedby={error ? errId : describedBy}
          onChange={(e) => {
            setText(e.target.value)
            if (error) setError(null)
          }}
          onKeyDown={onKeyDown}
          onPaste={onPaste}
          onBlur={() => commit(text)}
        />
      </div>
      {error && (
        <p id={errId} className="field-error" role="alert">
          {error}
        </p>
      )}
      {warned && <p className="field-warn">{WILDCARD_WARNING}</p>}
    </div>
  )
}
