import { useEffect, useRef, useState } from 'react'

/**
 * Polls the controller's read-only status API.
 *
 * On a failed poll it keeps whatever was last read and records the error. A
 * dashboard that blanks out because one request timed out is less useful than
 * one that says plainly that its data is stale.
 */
export function useGitSources(intervalMs = 2000) {
  const [state, setState] = useState({
    sources: null, // null until the first successful read
    error: null,
    lastUpdated: null,
  })

  useEffect(() => {
    let cancelled = false
    let timer

    async function poll() {
      try {
        const res = await fetch('/api/gitsources', {
          headers: { Accept: 'application/json' },
        })
        if (!res.ok) throw new Error(`the controller answered ${res.status}`)
        const data = await res.json()
        if (cancelled) return
        setState({ sources: data, error: null, lastUpdated: new Date() })
      } catch (err) {
        if (cancelled) return
        setState((prev) => ({ ...prev, error: describe(err) }))
      } finally {
        if (!cancelled) timer = setTimeout(poll, intervalMs)
      }
    }

    poll()
    return () => {
      cancelled = true
      clearTimeout(timer)
    }
  }, [intervalMs])

  return state
}

function describe(err) {
  if (err instanceof TypeError) return 'the controller is not answering'
  return err.message
}

/**
 * Accumulates the reconcile passes this page has watched happen.
 *
 * The API reports current state, not history, so the record only covers what
 * has been observed since the page was opened. Nothing here is inferred or
 * back-filled.
 */
export function usePassHistory(sources, max = 48) {
  const [history, setHistory] = useState({})
  const seen = useRef({})

  useEffect(() => {
    if (!sources) return

    setHistory((prev) => {
      const next = { ...prev }
      let changed = false

      for (const s of sources) {
        const key = `${s.namespace}/${s.name}`
        // Phase and message are part of the stamp because a failing sync does
        // not move lastSyncTime, and a failure is still a pass worth marking.
        const stamp = `${s.lastSyncTime ?? ''}|${s.phase}|${s.message}`
        if (seen.current[key] === stamp) continue
        seen.current[key] = stamp

        next[key] = [
          ...(next[key] ?? []),
          {
            at: s.lastSyncTime ? new Date(s.lastSyncTime) : new Date(),
            phase: s.phase,
            drifted: s.driftedObjects?.length ?? 0,
            commit: s.lastSyncedCommit,
            message: s.message,
          },
        ].slice(-max)
        changed = true
      }

      return changed ? next : prev
    })
  }, [sources, max])

  return history
}

/** A clock that ticks, so "12s ago" stays true without a poll. */
export function useNow(everyMs = 1000) {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), everyMs)
    return () => clearInterval(id)
  }, [everyMs])
  return now
}
