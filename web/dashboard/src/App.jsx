import { useGitSources, usePassHistory, useNow } from './api.js'

export default function App() {
  const { sources, error, lastUpdated } = useGitSources(2000)
  const history = usePassHistory(sources)
  const now = useNow(1000)

  return (
    <div className="sheet">
      <Masthead
        count={sources?.length}
        error={error}
        lastUpdated={lastUpdated}
        now={now}
      />

      <main>
        {sources === null && !error && <Skeleton />}
        {sources === null && error && <Unreachable error={error} />}
        {sources?.length === 0 && <Empty />}
        {sources?.map((s) => (
          <SourceRow
            key={`${s.namespace}/${s.name}`}
            source={s}
            passes={history[`${s.namespace}/${s.name}`] ?? []}
            now={now}
          />
        ))}
      </main>

      <footer>
        <p>
          Read-only. Every change to this cluster goes through Git; this page
          reports what the controller did and can alter nothing.
        </p>
        <p className="quiet">
          Runs on your machine. It reads the controller&rsquo;s status endpoint,
          stores nothing, and sends nothing anywhere.
        </p>
      </footer>
    </div>
  )
}

function Masthead({ count, error, lastUpdated, now }) {
  return (
    <header>
      <div className="wordmark">
        <span className="mark">mini&#8209;argo</span>
        <span className="eyebrow">sync status</span>
      </div>

      <dl className="readout">
        <div>
          <dt>sources</dt>
          <dd>{count ?? '–'}</dd>
        </div>
        <div>
          <dt>controller</dt>
          <dd className={error ? 'is-broken' : 'is-live'}>
            {error ? 'unreachable' : 'answering'}
          </dd>
        </div>
        <div>
          <dt>last read</dt>
          <dd>{lastUpdated ? ago(lastUpdated, now) : '–'}</dd>
        </div>
      </dl>
    </header>
  )
}

function SourceRow({ source, passes, now }) {
  const s = source
  const drifting = s.driftedObjects.length > 0

  return (
    <article className={`source is-${s.phase.toLowerCase()}`}>
      <div className="identity">
        <h2>{s.name}</h2>
        <p className="namespace">{s.namespace}</p>
      </div>

      <Phase phase={s.phase} />

      <dl className="facts">
        <div className="wide">
          <dt>repository</dt>
          <dd className="data">{shortRepo(s.repo)}</dd>
        </div>
        <div>
          <dt>branch</dt>
          <dd className="data">{s.branch}</dd>
        </div>
        <div>
          <dt>path</dt>
          <dd className="data">{s.path}</dd>
        </div>
        <div>
          <dt>commit</dt>
          <dd className="data">{s.lastSyncedCommit || 'not yet synced'}</dd>
        </div>
        <div>
          <dt>last sync</dt>
          <dd className="data">
            {s.lastSyncTime ? ago(new Date(s.lastSyncTime), now) : 'never'}
          </dd>
        </div>
        <div>
          <dt>checks</dt>
          <dd className="data">every {s.interval}</dd>
        </div>
        <div>
          <dt>prune</dt>
          <dd className="data">{s.prune ? 'on' : 'off'}</dd>
        </div>
      </dl>

      <TickTrack passes={passes} />

      {drifting && (
        <div className="corrections">
          <p className="label">corrected last pass</p>
          <ul>
            {s.driftedObjects.map((o) => (
              <li key={refKey(o)} className="data">
                {o.kind} <span className="quiet">/</span> {o.namespace}{' '}
                <span className="quiet">/</span> {o.name}
              </li>
            ))}
          </ul>
          {s.message && <p className="detail">{s.message}</p>}
        </div>
      )}

      {s.phase === 'Error' && s.message && (
        <p className="failure">{s.message}</p>
      )}

      <Inventory refs={s.inventory} />
    </article>
  )
}

function Phase({ phase }) {
  const said = {
    Synced: 'matches git',
    Drifted: 'corrected drift',
    Error: 'sync failed',
    Pending: 'not synced yet',
  }[phase]

  return (
    <p className="phase">
      <span className="phase-word">{phase}</span>
      <span className="phase-gloss">{said}</span>
    </p>
  )
}

/**
 * One mark per reconcile pass this page has watched happen. Quiet marks are
 * passes where nothing needed doing; tall marks are corrections.
 */
const TRACK_SLOTS = 48

function TickTrack({ passes }) {
  const slots = Array.from({ length: TRACK_SLOTS }, (_, i) => passes[i] ?? null)

  return (
    <div className="track">
      <p className="label">
        observed passes
        <span className="quiet"> since this page opened</span>
        <span className="count">{passes.length}</span>
      </p>

      <div className="ticks" role="img" aria-label={trackSummary(passes)}>
        {slots.map((p, i) =>
          p ? (
            <span
              key={i}
              className={`tick is-${p.phase.toLowerCase()}`}
              title={tickTitle(p)}
            />
          ) : (
            <span key={i} className="tick is-empty" />
          ),
        )}
      </div>

      {passes.length === 0 && (
        <p className="waiting">
          Nothing observed yet. The next pass appears here within one interval.
        </p>
      )}
    </div>
  )
}

function Inventory({ refs }) {
  if (refs.length === 0) return null
  return (
    <details className="inventory">
      <summary>
        owns {refs.length} {refs.length === 1 ? 'object' : 'objects'}
      </summary>
      <ul>
        {refs.map((o) => (
          <li key={refKey(o)} className="data">
            {o.kind} <span className="quiet">/</span> {o.namespace}{' '}
            <span className="quiet">/</span> {o.name}
          </li>
        ))}
      </ul>
    </details>
  )
}

function Skeleton() {
  return (
    <article className="source is-skeleton" aria-hidden="true">
      <div className="identity">
        <div className="ghost ghost-title" />
        <div className="ghost ghost-sub" />
      </div>
      <div className="ghost ghost-phase" />
      <div className="ghost-facts">
        {Array.from({ length: 6 }, (_, i) => (
          <div key={i} className="ghost ghost-fact" />
        ))}
      </div>
      <div className="ghost ghost-track" />
    </article>
  )
}

function Empty() {
  return (
    <article className="notice">
      <h2>No GitSources yet</h2>
      <p>Point one at a repository to start syncing:</p>
      <pre className="data">
        kubectl apply -f config/samples/gitops_v1alpha1_gitsource.yaml
      </pre>
    </article>
  )
}

function Unreachable({ error }) {
  return (
    <article className="notice is-broken">
      <h2>Not reaching the controller</h2>
      <p>The status API at :8082 {error}.</p>
      <p>Start it with:</p>
      <pre className="data">make run</pre>
    </article>
  )
}

/* helpers */

function refKey(o) {
  return `${o.group}/${o.version}/${o.kind}/${o.namespace}/${o.name}`
}

function shortRepo(url) {
  return url.replace(/^https?:\/\//, '').replace(/\.git$/, '')
}

function ago(date, now) {
  const seconds = Math.max(0, Math.round((now - date.getTime()) / 1000))
  if (seconds < 2) return 'just now'
  if (seconds < 60) return `${seconds}s ago`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`
  return `${Math.floor(minutes / 60)}h ago`
}

function tickTitle(p) {
  const at = p.at.toLocaleTimeString()
  if (p.phase === 'Error') return `${at} — sync failed: ${p.message}`
  if (p.drifted > 0) return `${at} — corrected ${p.drifted}: ${p.message}`
  return `${at} — nothing to do${p.commit ? `, at ${p.commit}` : ''}`
}

function trackSummary(passes) {
  if (passes.length === 0) return 'No passes observed yet'
  const corrected = passes.filter((p) => p.drifted > 0).length
  const failed = passes.filter((p) => p.phase === 'Error').length
  return `${passes.length} passes observed, ${corrected} corrected drift, ${failed} failed`
}
