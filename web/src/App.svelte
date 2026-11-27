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

  let status = $state<Status | null>(null)
  let user = $state('admin')
  let pass = $state('')
  let needsAuth = $state(false)
  let error = $state('')
  let loadedAt = $state('')

  async function load(): Promise<void> {
    try {
      const res = await fetch('/api/status', {
        headers: { Authorization: 'Basic ' + btoa(`${user}:${pass}`) },
      })
      if (res.status === 401) {
        needsAuth = true
        error = 'Invalid credentials'
        return
      }
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

  function login(event: SubmitEvent): void {
    event.preventDefault()
    load()
  }

  onMount(() => {
    void load()
    const timer = setInterval(() => void load(), 5000)
    return () => clearInterval(timer)
  })
</script>

<header>
  <div class="brand">
    <span class="dot"></span>
    <h1>go-reverse-flow</h1>
  </div>
  <span class="meta">v{status?.version ?? '…'} · admin</span>
</header>

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
  <main>
    {#if error}<p class="error">{error} — retrying…</p>{/if}

    <section class="cards">
      <div class="card">
        <h3>Proxy</h3>
        <p class="value">{status.listen}</p>
        <p class="hint">uptime {status.uptime}</p>
      </div>
      <div class="card">
        <h3>Backends</h3>
        <p class="value">
          {status.backends.filter((b) => b.healthy).length}/{status.backends.length}
          <span class="hint">healthy</span>
        </p>
      </div>
      <div class="card">
        <h3>Cache</h3>
        <p class="value">{status.cache_ttl === '0s' ? 'off' : status.cache_ttl}</p>
        <p class="hint">ttl</p>
      </div>
      <div class="card">
        <h3>Rate limit</h3>
        <p class="value">
          {status.rate_limit > 0 ? `${status.rate_limit}/s` : 'off'}
          {status.rate_limit > 0 ? `<span class="hint">burst {status.rate_burst}</span>` : ''}
        </p>
      </div>
    </section>

    <section class="card">
      <h3>Backends</h3>
      <table>
        <thead>
          <tr><th>Backend</th><th>Health</th><th>Requests</th></tr>
        </thead>
        <tbody>
          {#each status.backends as b (b.url)}
            <tr>
              <td>{b.url}</td>
              <td>
                <span class="dot {b.healthy ? 'up' : 'down'}"></span>
                {b.healthy ? 'healthy' : 'down'}
              </td>
              <td>{b.requests}</td>
            </tr>
          {/each}
        </tbody>
      </table>
      <p class="hint">auto-refreshes every 5s · updated {loadedAt}</p>
    </section>

    <section class="card">
      <h3>Runtime</h3>
      <p class="hint">
        go {status.go_version} · proxy on {status.listen} · admin on {status.admin_listen}
        · started {new Date(status.started_at).toLocaleString()}
      </p>
      <p class="hint">
        <a href="/metrics" target="_blank" rel="noreferrer">/metrics</a>
        {#if status.cache_ttl !== '0s'}
          · <form class="inline" method="post" action="/cache/purge"><button type="submit">Purge cache</button></form>
        {/if}
      </p>
    </section>
  </main>
{:else}
  <main><p class="hint">loading…</p></main>
{/if}

<style>
  :global(body) {
    margin: 0;
    font-family: system-ui, -apple-system, 'Segoe UI', sans-serif;
    background: #f5f6f8;
    color: #1f2430;
  }
  header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 14px 24px;
    background: #ffffff;
    border-bottom: 1px solid #e3e6ea;
  }
  .brand { display: flex; align-items: center; gap: 10px; }
  .brand h1 { font-size: 18px; font-weight: 600; margin: 0; }
  .dot {
    width: 10px; height: 10px; border-radius: 50%;
    display: inline-block; background: #4c7ef3;
  }
  .dot.up { background: #2fa96e; }
  .dot.down { background: #d64545; }
  .meta { color: #6b7280; font-size: 13px; }
  main {
    max-width: 860px;
    margin: 24px auto;
    padding: 0 16px;
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .card {
    background: #ffffff;
    border: 1px solid #e3e6ea;
    border-radius: 10px;
    padding: 16px 20px;
  }
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: 16px;
  }
  h3 { margin: 0 0 8px; font-size: 13px; font-weight: 600; color: #6b7280; text-transform: uppercase; letter-spacing: 0.04em; }
  .value { margin: 0; font-size: 22px; font-weight: 600; }
  .hint { margin: 4px 0 0; color: #6b7280; font-size: 13px; }
  .error { color: #d64545; }
  table { width: 100%; border-collapse: collapse; }
  th, td { text-align: left; padding: 8px 6px; border-bottom: 1px solid #eef0f2; font-size: 14px; }
  th { color: #6b7280; font-weight: 500; }
  input {
    display: block; width: 100%; box-sizing: border-box;
    margin: 4px 0 12px; padding: 8px 10px;
    border: 1px solid #d3d7dd; border-radius: 6px; font-size: 14px;
  }
  button {
    background: #4c7ef3; color: #fff; border: 0; border-radius: 6px;
    padding: 8px 14px; font-size: 14px; cursor: pointer;
  }
  button:hover { background: #3d6de0; }
  .inline { display: inline; margin: 0; }
  a { color: #4c7ef3; }
</style>
