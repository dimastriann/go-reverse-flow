<script lang="ts">
  import { onMount } from 'svelte'

  type Backend = {
    url: string
    healthy: boolean
    requests: number
  }

  type Status = {
    version: string
    go_version: string
    uptime: string
    listen: string
    admin_listen: string
    started_at: string
    backends: Backend[]
    cache_ttl: string
    rate_limit: number
    rate_burst: number
  }

  type Route = {
    prefix: string
    backends: string[]
  }

  type Config = {
    listen_addr: string
    backends: string[]
    backend_weights?: number[]
    routes?: Route[]
    cache?: { ttl: string }
    rate_limiter?: { rate: number; burst: number }
    admin?: { listen?: string; username?: string }
  }

  type LogEntry = {
    time: string
    remote: string
    method: string
    path: string
    backend: string
    status: number
    duration: string
  }

  type Tab = 'dashboard' | 'config' | 'logs'

  let tab = $state<Tab>('dashboard')
  let status = $state<Status | null>(null)
  let user = $state('admin')
  let pass = $state('')
  let needsAuth = $state(false)
  let error = $state('')
  let loadedAt = $state('')
  let toast = $state('')
  let toastKind = $state<'ok' | 'err'>('ok')

  let config = $state<Config | null>(null)
  let draft = $state<Config | null>(null)
  let configError = $state('')
  let saving = $state(false)

  let logs = $state<LogEntry[]>([])

  let canPurge = $derived(status !== null && status.cache_ttl !== '0s')

  function authHeader(): Record<string, string> {
    return { Authorization: 'Basic ' + btoa(`${user}:${pass}`) }
  }

  async function api(path: string, init: RequestInit = {}): Promise<Response> {
    const res = await fetch(path, {
      ...init,
      headers: { ...authHeader(), ...(init.headers ?? {}) },
    })
    if (res.status === 401) {
      needsAuth = true
      throw new Error('Invalid credentials')
    }
    return res
  }

  function show(message: string, kind: 'ok' | 'err' = 'ok'): void {
    toast = message
    toastKind = kind
    setTimeout(() => (toast = ''), 4000)
  }

  async function loadStatus(): Promise<void> {
    try {
      const res = await api('/api/status')
      if (!res.ok) {
        error = `status endpoint returned ${res.status}`
        return
      }
      status = await res.json()
      needsAuth = false
      error = ''
      loadedAt = new Date().toLocaleTimeString()
    } catch {
      error = 'admin API unreachable'
    }
  }

  async function loadConfig(): Promise<void> {
    const res = await api('/api/config')
    if (res.ok) {
      config = await res.json()
      draft = structuredClone($state.snapshot(config))
      configError = ''
    }
  }

  async function loadLogs(): Promise<void> {
    const res = await api('/api/logs?limit=100')
    if (res.ok) {
      logs = await res.json()
    }
  }

  async function saveConfig(): Promise<void> {
    if (!draft) return
    saving = true
    try {
      const res = await fetch('/api/config', {
        method: 'PUT',
        headers: { ...authHeader(), 'Content-Type': 'application/json' },
        body: JSON.stringify(draft),
      })
      const body = await res.json()
      if (res.status === 401) {
        needsAuth = true
        configError = 'Invalid credentials'
        return
      }
      if (!res.ok) {
        configError = body.error ?? `apply returned ${res.status}`
        return
      }
      configError = ''
      show('Configuration applied and persisted')
      await loadStatus()
      await loadConfig()
    } finally {
      saving = false
    }
  }

  async function reloadConfig(): Promise<void> {
    const res = await fetch('/api/reload', { method: 'POST', headers: authHeader() })
    const body = await res.json()
    if (res.status === 401) {
      needsAuth = true
      show('Invalid credentials', 'err')
      return
    }
    if (!res.ok) {
      show(body.error ?? `reload returned ${res.status}`, 'err')
      return
    }
    show('Reloaded from file')
    await loadStatus()
    await loadConfig()
  }

  async function purgeCache(): Promise<void> {
    const res = await fetch('/cache/purge', { method: 'POST', headers: authHeader() })
    if (res.status === 401) {
      needsAuth = true
      show('Invalid credentials', 'err')
      return
    }
    show(res.ok ? 'Cache purged' : `Purge returned ${res.status}`, res.ok ? 'ok' : 'err')
  }

  function login(event: SubmitEvent): void {
    event.preventDefault()
    loadStatus()
  }

  function switchTo(next: Tab): void {
    tab = next
    if (next === 'config') void loadConfig()
    if (next === 'logs') void loadLogs()
  }

  onMount(() => {
    void loadStatus()
    const timer = setInterval(() => {
      void loadStatus()
      if (tab === 'logs') void loadLogs()
    }, 5000)
    return () => clearInterval(timer)
  })

  function addBackend(): void {
    if (!draft) return
    draft.backends.push('')
  }

  function removeBackend(i: number): void {
    if (!draft) return
    draft.backends.splice(i, 1)
    if (draft.backend_weights) draft.backend_weights.splice(i, 1)
  }

  function addRoute(): void {
    if (!draft) return
    if (!draft.routes) draft.routes = []
    draft.routes.push({ prefix: '/', backends: [] })
  }

  function removeRoute(i: number): void {
    if (!draft) return
    draft.routes?.splice(i, 1)
  }

  function healthyCount(): string {
    if (!status) return '0/0'
    return `${status.backends.filter((b) => b.healthy).length}/${status.backends.length}`
  }
</script>

<header>
  <div class="brand">
    <span class="brand-dot"></span>
    <h1>go-reverse-flow</h1>
    <span class="badge">{status?.version ?? '…'}</span>
  </div>
  <nav>
    <button class="tab" class:active={tab === 'dashboard'} on:click={() => switchTo('dashboard')}>
      Dashboard
    </button>
    <button class="tab" class:active={tab === 'config'} on:click={() => switchTo('config')}>
      Config
    </button>
    <button class="tab" class:active={tab === 'logs'} on:click={() => switchTo('logs')}>
      Logs
    </button>
  </nav>
  <span class="meta">{status?.uptime ?? ''} · admin</span>
</header>

{#if toast}
  <div class="toast {toastKind}">{toast}</div>
{/if}

{#if needsAuth}
  <main>
    <form class="card login" on:submit={login}>
      <h2>Sign in</h2>
      {#if error}<p class="error">{error}</p>{/if}
      <label>
        User
        <input type="text" bind:value={user} autocomplete="username" />
      </label>
      <label>
        Password
        <input type="password" bind:value={pass} autocomplete="current-password" />
      </label>
      <button type="submit">Sign in</button>
    </form>
  </main>
{:else if status}
  {#if tab === 'dashboard'}
    <main>
      {#if error}<p class="error banner">{error} — retrying…</p>{/if}

      <section class="cards">
        <div class="card">
          <h3>Proxy</h3>
          <p class="value">{status.listen}</p>
          <p class="hint">uptime {status.uptime}</p>
        </div>
        <div class="card">
          <h3>Backends</h3>
          <p class="value">{healthyCount()} <span class="hint">healthy</span></p>
        </div>
        <div class="card">
          <h3>Cache</h3>
          <p class="value">{status.cache_ttl === '0s' ? 'off' : status.cache_ttl}</p>
          <p class="hint">ttl</p>
        </div>
        <div class="card">
          <h3>Rate limit</h3>
          <p class="value">
            {#if status.rate_limit > 0}
              {status.rate_limit}/s
              <span class="hint">burst {status.rate_burst}</span>
            {:else}
              off
            {/if}
          </p>
        </div>
      </section>

      <section class="card">
        <div class="row">
          <h3>Backends</h3>
          <span class="hint">auto-refresh 5s · updated {loadedAt}</span>
        </div>
        <table>
          <thead>
            <tr><th>Backend</th><th>Health</th><th>Requests</th></tr>
          </thead>
          <tbody>
            {#each status.backends as b (b.url)}
              <tr>
                <td class="mono">{b.url}</td>
                <td><span class="pill {b.healthy ? 'up' : 'down'}">{b.healthy ? 'healthy' : 'down'}</span></td>
                <td class="mono">{b.requests}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </section>

      <section class="card">
        <h3>Operations</h3>
        <div class="ops">
          {#if canPurge}
            <button on:click={purgeCache}>Purge cache</button>
          {/if}
          <button on:click={reloadConfig}>Reload from file</button>
          <a class="button" href="/metrics" target="_blank" rel="noreferrer">/metrics</a>
        </div>
      </section>
    </main>
  {:else if tab === 'config'}
    <main>
      {#if draft}
        {#if configError}<p class="error banner">{configError}</p>{/if}

        <section class="card">
          <div class="row">
            <h3>Default pool</h3>
            <span class="hint">backend_weights aligns with this list</span>
          </div>
          {#each draft.backends as backend, i}
            <div class="line">
              <input class="mono" bind:value={draft.backends[i]} placeholder="http://host:port" />
              {#if draft.backend_weights}
                <input
                  class="weight mono"
                  type="number"
                  min="1"
                  bind:value={draft.backend_weights[i]}
                  title="weight"
                />
              {/if}
              <button class="danger" on:click={() => removeBackend(i)}>×</button>
            </div>
          {/each}
          <button class="ghost" on:click={addBackend}>+ Add backend</button>
        </section>

        <section class="card">
          <div class="row">
            <h3>Routes</h3>
            <span class="hint">longest prefix wins, path forwarded as-is</span>
          </div>
          {#if !draft.routes || draft.routes.length === 0}
            <p class="hint">No routes — every request uses the default pool.</p>
          {/if}
          {#each draft.routes ?? [] as route, i}
            <div class="route">
              <div class="line">
                <input class="mono prefix" bind:value={route.prefix} placeholder="/prefix" />
                <button class="danger" on:click={() => removeRoute(i)}>×</button>
              </div>
              <input
                class="mono"
                bind:value={route.backends}
                placeholder="http://a:1, http://b:2"
              />
            </div>
          {/each}
          <button class="ghost" on:click={addRoute}>+ Add route</button>
        </section>

        <section class="card">
          <div class="row">
            <h3>Fixed at runtime</h3>
            <span class="hint">changing these needs a file edit + restart</span>
          </div>
          <p class="hint">
            listen_addr <span class="mono">{config?.listen_addr}</span> · admin
            <span class="mono">{config?.admin?.listen}</span>
          </p>
        </section>

        <section class="actions">
          <button disabled={saving} on:click={saveConfig}>
            {saving ? 'Applying…' : 'Apply & persist'}
          </button>
          <button class="ghost" on:click={reloadConfig}>Reload from file</button>
          <span class="hint">.bak of the previous file is kept on every apply</span>
        </section>
      {:else}
        <p class="hint">loading config…</p>
      {/if}
    </main>
  {:else if tab === 'logs'}
    <main>
      <section class="card">
        <div class="row">
          <h3>Access log</h3>
          <span class="hint">last {logs.length} requests · auto-refresh 5s</span>
        </div>
        {#if logs.length === 0}
          <p class="hint">No requests recorded yet.</p>
        {:else}
          <table>
            <thead>
              <tr><th>Time</th><th>Method</th><th>Path</th><th>Backend</th><th>Status</th><th>Duration</th></tr>
            </thead>
            <tbody>
              {#each logs as e}
                <tr>
                  <td class="mono">{new Date(e.time).toLocaleTimeString()}</td>
                  <td>{e.method}</td>
                  <td class="mono">{e.path}</td>
                  <td class="mono">{e.backend || '—'}</td>
                  <td><span class="pill {e.status < 400 ? 'up' : 'down'}">{e.status}</span></td>
                  <td class="mono">{e.duration}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      </section>
    </main>
  {/if}
{:else}
  <main><p class="hint">loading…</p></main>
{/if}

<style>
  :global(body) {
    margin: 0;
    font-family: system-ui, -apple-system, 'Segoe UI', sans-serif;
    background: #0d1117;
    color: #e6edf3;
  }
  header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 16px;
    padding: 12px 24px;
    background: linear-gradient(180deg, #161b22, #0d1117);
    border-bottom: 1px solid #232a34;
    position: sticky;
    top: 0;
    z-index: 5;
  }
  .brand { display: flex; align-items: center; gap: 10px; }
  .brand h1 { font-size: 17px; font-weight: 600; margin: 0; }
  .brand-dot {
    width: 10px; height: 10px; border-radius: 50%;
    background: linear-gradient(135deg, #4c7ef3, #7c5cff);
  }
  .badge {
    background: #1c2430; border: 1px solid #2b3644;
    padding: 2px 8px; border-radius: 999px;
    font-size: 12px; color: #8b949e;
  }
  nav { display: flex; gap: 6px; }
  .tab {
    background: transparent; color: #8b949e; border: 1px solid transparent;
    border-radius: 8px; padding: 6px 14px; font-size: 14px; cursor: pointer;
  }
  .tab:hover { color: #e6edf3; background: #1c2430; }
  .tab.active { color: #e6edf3; background: #1c2430; border-color: #2b3644; }
  .meta { color: #8b949e; font-size: 13px; }
  main {
    max-width: 900px;
    margin: 24px auto;
    padding: 0 16px 40px;
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .card {
    background: #161b22;
    border: 1px solid #232a34;
    border-radius: 12px;
    padding: 16px 20px;
  }
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: 16px;
  }
  h3 {
    margin: 0 0 8px; font-size: 12px; font-weight: 600;
    color: #8b949e; text-transform: uppercase; letter-spacing: 0.05em;
  }
  .value { margin: 0; font-size: 22px; font-weight: 600; }
  .hint { margin: 4px 0 0; color: #8b949e; font-size: 13px; }
  .error { color: #f85149; }
  .banner {
    background: #2d1114; border: 1px solid #f8514933;
    border-radius: 8px; padding: 10px 14px; font-size: 14px;
  }
  .row { display: flex; justify-content: space-between; align-items: baseline; gap: 12px; }
  table { width: 100%; border-collapse: collapse; }
  th, td { text-align: left; padding: 8px 6px; border-bottom: 1px solid #1f2630; font-size: 14px; }
  th { color: #8b949e; font-weight: 500; }
  .mono { font-family: ui-monospace, 'Cascadia Code', Consolas, monospace; font-size: 13px; }
  .pill {
    padding: 2px 10px; border-radius: 999px; font-size: 12px;
    border: 1px solid transparent;
  }
  .pill.up { background: #12261a; color: #3fb950; border-color: #3fb95044; }
  .pill.down { background: #2d1114; color: #f85149; border-color: #f8514944; }
  .ops, .actions { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; }
  button, .button {
    background: #1c2430; color: #e6edf3; border: 1px solid #2b3644;
    border-radius: 8px; padding: 8px 14px; font-size: 14px; cursor: pointer;
    text-decoration: none;
  }
  button:hover, .button:hover { border-color: #4c7ef3; color: #ffffff; }
  button:disabled { opacity: 0.5; cursor: wait; }
  .button { display: inline-block; }
  .ghost { background: transparent; }
  .danger { background: transparent; color: #f85149; border-color: transparent; padding: 4px 10px; }
  .danger:hover { border-color: #f8514966; color: #ff7b72; }
  input {
    background: #0d1117; color: #e6edf3; border: 1px solid #2b3644;
    border-radius: 8px; padding: 8px 10px; font-size: 13px;
  }
  input:focus { outline: none; border-color: #4c7ef3; }
  label {
    display: block; margin: 10px 0 4px; font-size: 13px; color: #8b949e;
  }
  .line { display: flex; gap: 8px; align-items: center; margin-bottom: 8px; }
  .line input { flex: 1; }
  .line .weight { width: 80px; flex: none; }
  .prefix { width: 200px; flex: none; }
  .route {
    border: 1px solid #232a34; border-radius: 10px;
    padding: 10px 12px; margin-bottom: 10px;
    display: flex; flex-direction: column; gap: 8px;
  }
  .route .mono { width: 100%; box-sizing: border-box; }
  .toast {
    position: fixed; top: 64px; right: 24px; z-index: 20;
    padding: 10px 16px; border-radius: 10px; font-size: 14px;
  }
  .toast.ok { background: #12261a; color: #3fb950; border: 1px solid #3fb95044; }
  .toast.err { background: #2d1114; color: #f85149; border: 1px solid #f8514944; }
  a { color: #4c7ef3; }
</style>
