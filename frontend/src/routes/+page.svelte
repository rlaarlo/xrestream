<script lang="ts">
  import { onMount, onDestroy, tick } from 'svelte';
  import Hls from 'hls.js';
  import {
    apiFetch,
    getApiBase,
    getStoredToken,
    setStoredToken,
    createShareLink,
    type Channel,
    type ChannelInput,
    type Metrics,
    type ShareLink
  } from '$lib/api';

  type Mode = ChannelInput['mode'];

  const MODE_LABEL: Record<Mode, string> = {
    transmux: 'Transmux',
    ingest: 'Ingest',
    proxy: 'Proxy'
  };

  const MODE_DESC: Record<Mode, string> = {
    transmux: 'Direct stream (TS / non-HLS) → FFmpeg -c copy → HLS. 1 connection to source.',
    ingest: 'Source is already HLS. Backend polls playlist and prefetches segments.',
    proxy: 'On-demand proxy to HLS source, with singleflight + cache.'
  };

  let channels: Channel[] = [];
  let metrics: Record<string, Metrics> = {};
  let loading = true;
  let saving = false;
  let loggingIn = false;
  let error = '';
  let notice = '';
  let username = 'admin';
  let password = '';
  let authenticated = false;
  let showAdvanced = false;

  let form: ChannelInput = blankForm();
  let editingId = '';
  let showForm = false;

  let previewChannel: Channel | null = null;
  let videoEl: HTMLVideoElement | null = null;
  let hls: Hls | null = null;
  let refreshTimer: ReturnType<typeof setInterval> | null = null;

  let shareChannel: Channel | null = null;
  let shareTtl = 240;
  let shareResult: ShareLink | null = null;
  let shareLoading = false;

  function blankForm(): ChannelInput {
    return {
      name: '',
      slug: '',
      inputUrl: '',
      mode: 'transmux',
      status: 'active',
      playlistTtlSeconds: 2,
      segmentTtlSeconds: 120,
      ingestPollSeconds: 2,
      cacheEnabled: true,
      syncEnabled: false,
      syncDelaySeconds: 30
    };
  }

  onMount(() => {
    authenticated = Boolean(getStoredToken());
    if (authenticated) {
      void loadChannels();
      refreshTimer = setInterval(() => void refreshMetrics(), 5000);
    } else {
      loading = false;
    }
  });

  onDestroy(() => {
    if (refreshTimer) clearInterval(refreshTimer);
    closePlayer();
  });

  async function login() {
    loggingIn = true;
    error = '';
    notice = '';
    try {
      const response = await apiFetch<{ token: string }>('/auth/login', {
        method: 'POST',
        body: JSON.stringify({ username, password })
      });
      setStoredToken(response.token);
      authenticated = true;
      password = '';
      await loadChannels();
      if (!refreshTimer) refreshTimer = setInterval(() => void refreshMetrics(), 5000);
    } catch (err) {
      error = messageFrom(err);
    } finally {
      loggingIn = false;
    }
  }

  async function loadChannels() {
    loading = true;
    error = '';
    try {
      channels = await apiFetch<Channel[]>('/channels');
      await Promise.all(channels.map(loadMetrics));
    } catch (err) {
      error = messageFrom(err);
    } finally {
      loading = false;
    }
  }

  async function refreshMetrics() {
    if (!channels.length) return;
    try {
      const fresh = await apiFetch<Channel[]>('/channels');
      channels = fresh;
      await Promise.all(fresh.map(loadMetrics));
    } catch {
      /* ignore transient errors */
    }
  }

  async function loadMetrics(channel: Channel) {
    try {
      metrics[channel.id] = await apiFetch<Metrics>(`/channels/${channel.id}/metrics`);
      metrics = { ...metrics };
    } catch {
      metrics[channel.id] = {
        playlistRequests: 0,
        segmentRequests: 0,
        upstreamRequests: 0,
        cacheHits: 0,
        cacheMisses: 0,
        bytesSent: 0,
        bytesUpstream: 0,
        workerErrors: 0,
        liveViewers: 0
      };
    }
  }

  async function saveChannel() {
    saving = true;
    error = '';
    notice = '';
    try {
      if (editingId) {
        await apiFetch<Channel>(`/channels/${editingId}`, {
          method: 'PATCH',
          body: JSON.stringify(form)
        });
        notice = 'Channel updated.';
      } else {
        await apiFetch<Channel>('/channels', {
          method: 'POST',
          body: JSON.stringify(form)
        });
        notice = 'Channel created.';
      }
      resetForm();
      await loadChannels();
    } catch (err) {
      error = messageFrom(err);
    } finally {
      saving = false;
    }
  }

  function editChannel(channel: Channel) {
    editingId = channel.id;
    form = {
      name: channel.name,
      slug: channel.slug,
      inputUrl: channel.inputUrl,
      mode: channel.mode,
      status: channel.status,
      playlistTtlSeconds: channel.playlistTtlSeconds,
      segmentTtlSeconds: channel.segmentTtlSeconds,
      ingestPollSeconds: channel.ingestPollSeconds,
      cacheEnabled: channel.cacheEnabled,
      syncEnabled: channel.syncEnabled,
      syncDelaySeconds: channel.syncDelaySeconds
    };
    showForm = true;
  }

  function openCreateForm() {
    editingId = '';
    form = blankForm();
    showAdvanced = false;
    showForm = true;
  }

  function resetForm() {
    editingId = '';
    form = blankForm();
    showForm = false;
  }

  async function removeChannel(channel: Channel) {
    if (!confirm(`Delete channel "${channel.name}"?`)) return;
    error = '';
    try {
      await apiFetch(`/channels/${channel.id}`, { method: 'DELETE' });
      notice = 'Channel deleted.';
      await loadChannels();
    } catch (err) {
      error = messageFrom(err);
    }
  }

  async function workerAction(channel: Channel, action: 'start' | 'stop') {
    error = '';
    try {
      await apiFetch(`/channels/${channel.id}/worker/${action}`, { method: 'POST' });
      await loadChannels();
    } catch (err) {
      error = messageFrom(err);
    }
  }

  async function purgeCache(channel: Channel) {
    error = '';
    try {
      await apiFetch(`/channels/${channel.id}/purge-cache`, { method: 'POST' });
      notice = 'Channel cache cleared.';
    } catch (err) {
      error = messageFrom(err);
    }
  }

  async function copy(text = '') {
    if (!text) return;
    try {
      await navigator.clipboard.writeText(text);
      notice = 'Link copied to clipboard.';
    } catch {
      notice = '';
      error = 'Failed to copy to clipboard.';
    }
  }

  function logout() {
    setStoredToken('');
    authenticated = false;
    channels = [];
    metrics = {};
    if (refreshTimer) {
      clearInterval(refreshTimer);
      refreshTimer = null;
    }
    closePlayer();
  }

  async function openPlayer(channel: Channel) {
    if (!channel.playlistUrl) return;
    previewChannel = channel;
    await tick();
    if (!videoEl) return;
    closeHls();
    if (Hls.isSupported()) {
      hls = new Hls({ lowLatencyMode: false, liveSyncDuration: 6 });
      hls.loadSource(channel.playlistUrl);
      hls.attachMedia(videoEl);
    } else if (videoEl.canPlayType('application/vnd.apple.mpegurl')) {
      videoEl.src = channel.playlistUrl;
    } else {
      error = 'Browser does not support HLS. Use the latest Chrome/Firefox or VLC.';
    }
  }

  function closePlayer() {
    previewChannel = null;
    closeHls();
    if (videoEl) {
      videoEl.pause();
      videoEl.removeAttribute('src');
      videoEl.load();
    }
  }

  function closeHls() {
    if (hls) {
      hls.destroy();
      hls = null;
    }
  }

  function openShare(channel: Channel) {
    shareChannel = channel;
    shareResult = null;
    shareTtl = 240;
  }

  async function generateShare() {
    if (!shareChannel) return;
    shareLoading = true;
    error = '';
    try {
      shareResult = await createShareLink(shareChannel.id, shareTtl);
    } catch (err) {
      error = messageFrom(err);
    } finally {
      shareLoading = false;
    }
  }

  function closeShare() {
    shareChannel = null;
    shareResult = null;
  }

  function autoSlug() {
    if (form.slug) return;
    form.slug = form.name
      .toLowerCase()
      .trim()
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-+|-+$/g, '');
  }

  function bytes(value = 0) {
    if (value < 1024) return `${value} B`;
    if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`;
    if (value < 1024 * 1024 * 1024) return `${(value / 1024 / 1024).toFixed(1)} MB`;
    return `${(value / 1024 / 1024 / 1024).toFixed(2)} GB`;
  }

  function relTime(iso?: string) {
    if (!iso) return '';
    const t = new Date(iso).getTime();
    if (!t) return '';
    const diff = Math.max(0, Date.now() - t);
    const s = Math.floor(diff / 1000);
    if (s < 60) return `${s}s ago`;
    const m = Math.floor(s / 60);
    if (m < 60) return `${m}m ago`;
    const h = Math.floor(m / 60);
    if (h < 24) return `${h}h ${m % 60}m ago`;
    const d = Math.floor(h / 24);
    return `${d}d ${h % 24}h ago`;
  }

  function uptime(iso?: string) {
    if (!iso) return '';
    const t = new Date(iso).getTime();
    if (!t) return '';
    const diff = Math.max(0, Date.now() - t);
    const s = Math.floor(diff / 1000);
    if (s < 60) return `${s}s`;
    const m = Math.floor(s / 60);
    if (m < 60) return `${m}m`;
    const h = Math.floor(m / 60);
    if (h < 24) return `${h}h ${m % 60}m`;
    const d = Math.floor(h / 24);
    return `${d}d ${h % 24}h`;
  }

  function srcStatusColor(code?: number) {
    if (!code) return 'text-base-content/40';
    if (code >= 200 && code < 300) return 'text-success';
    if (code >= 300 && code < 400) return 'text-info';
    return 'text-error';
  }

  function modeBadge(mode: Mode) {
    if (mode === 'transmux') return 'badge-primary';
    if (mode === 'ingest') return 'badge-info';
    return 'badge-ghost';
  }

  function statusBadge(status: Channel['status']) {
    if (status === 'active') return 'badge-success';
    if (status === 'source_error') return 'badge-error';
    return 'badge-ghost';
  }

  function workerBadge(worker: Channel['workerStatus']) {
    if (worker === 'running') return 'badge-success';
    if (worker === 'error') return 'badge-error';
    return 'badge-ghost';
  }

  function messageFrom(err: unknown) {
    return err instanceof Error ? err.message : 'An error occurred.';
  }

  $: runningCount = channels.filter((c) => c.workerStatus === 'running').length;
  $: errorCount = channels.filter((c) => c.workerStatus === 'error').length;
  $: totalSent = Object.values(metrics).reduce((sum, m) => sum + (m?.bytesSent || 0), 0);
</script>

<svelte:head>
  <title>Restream Dashboard</title>
</svelte:head>

<main class="min-h-screen bg-base-200 text-base-content">
  {#if !authenticated}
    <div class="flex min-h-screen items-center justify-center px-4 py-8">
      <div class="card w-full max-w-sm bg-base-100 shadow-lg">
        <div class="card-body">
          <h1 class="card-title">Restream Control</h1>
          <p class="text-sm text-base-content/70">Sign in as admin to manage relay channels.</p>

          {#if error}
            <div class="alert alert-error mt-2 text-sm">{error}</div>
          {/if}

          <form class="mt-2 grid gap-3" on:submit|preventDefault={login}>
            <label class="form-control">
              <span class="label-text">Username</span>
              <input class="input input-bordered" bind:value={username} autocomplete="username" required />
            </label>
            <label class="form-control">
              <span class="label-text">Password</span>
              <input
                class="input input-bordered"
                bind:value={password}
                autocomplete="current-password"
                required
                type="password"
              />
            </label>
            <button class="btn btn-primary mt-1" disabled={loggingIn} type="submit">
              {loggingIn ? 'Signing in…' : 'Sign in'}
            </button>
          </form>
        </div>
      </div>
    </div>
  {:else}
    <div class="border-b border-base-300 bg-base-100">
      <div
        class="mx-auto flex max-w-7xl flex-col gap-3 px-4 py-4 sm:flex-row sm:items-center sm:justify-between"
      >
        <div>
          <h1 class="text-xl font-semibold">Restream Control</h1>
          <p class="text-sm text-base-content/60">HLS relay without re-encode • .m3u8 output for any player.</p>
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <span class="badge badge-ghost">API: {getApiBase()}</span>
          <button class="btn btn-ghost btn-sm" type="button" on:click={loadChannels}>Refresh</button>
          <button class="btn btn-outline btn-sm" type="button" on:click={logout}>Logout</button>
        </div>
      </div>
    </div>

    <div class="mx-auto max-w-7xl px-4 py-6">
      <div class="mb-4 grid gap-3 md:grid-cols-4">
        <div class="stat rounded-box bg-base-100 shadow-sm">
          <div class="stat-title">Channels</div>
          <div class="stat-value text-2xl">{channels.length}</div>
        </div>
        <div class="stat rounded-box bg-base-100 shadow-sm">
          <div class="stat-title">Worker Running</div>
          <div class="stat-value text-2xl text-success">{runningCount}</div>
        </div>
        <div class="stat rounded-box bg-base-100 shadow-sm">
          <div class="stat-title">Worker Error</div>
          <div class="stat-value text-2xl" class:text-error={errorCount > 0}>{errorCount}</div>
        </div>
        <div class="stat rounded-box bg-base-100 shadow-sm">
          <div class="stat-title">Bytes Sent (1h)</div>
          <div class="stat-value text-2xl">{bytes(totalSent)}</div>
        </div>
      </div>

      {#if error}
        <div class="alert alert-error mb-4">
          <span>{error}</span>
          <button class="btn btn-ghost btn-xs" type="button" on:click={() => (error = '')}>×</button>
        </div>
      {/if}
      {#if notice}
        <div class="alert alert-success mb-4">
          <span>{notice}</span>
          <button class="btn btn-ghost btn-xs" type="button" on:click={() => (notice = '')}>×</button>
        </div>
      {/if}

      <div class="mb-3 flex items-center justify-between gap-2">
        <h2 class="text-lg font-semibold">Channels</h2>
        <button class="btn btn-primary btn-sm" type="button" on:click={openCreateForm}>
          + New Channel
        </button>
      </div>

      <section class="min-w-0">
        <div class="card bg-base-100 shadow-sm">
          <div class="card-body p-0">
            <div class="overflow-x-auto">
              <table class="table table-sm">
                  <thead>
                    <tr>
                      <th>Channel</th>
                      <th>Mode</th>
                      <th>Status</th>
                      <th>Worker</th>
                      <th>Playlist</th>
                      <th>Traffic</th>
                      <th class="text-right">Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#if loading}
                      <tr>
                        <td colspan="7" class="py-8 text-center text-base-content/60">Loading channels…</td>
                      </tr>
                    {:else if channels.length === 0}
                      <tr>
                        <td colspan="7" class="py-8 text-center text-base-content/60">
                          No channels yet. Create your first channel on the left.
                        </td>
                      </tr>
                    {:else}
                      {#each channels as channel (channel.id)}
                        <tr>
                          <td>
                            <div class="font-medium">{channel.name}</div>
                            <div class="text-xs text-base-content/60">{channel.slug}</div>
                            {#if channel.lastError}
                              <div
                                class="mt-1 max-w-[18rem] truncate text-xs text-error"
                                title={channel.lastError}
                              >
                                ⚠ {channel.lastError}
                              </div>
                            {/if}
                          </td>
                          <td>
                            <span class="badge {modeBadge(channel.mode)} badge-sm">
                              {MODE_LABEL[channel.mode]}
                            </span>
                            {#if channel.syncEnabled}
                              <span
                                class="badge badge-warning badge-sm ml-1"
                                title="Sync active ({channel.syncDelaySeconds}s delay)"
                              >
                                SYNC
                              </span>
                            {/if}
                          </td>
                          <td>
                            <span class="badge {statusBadge(channel.status)} badge-sm">{channel.status}</span>
                          </td>
                          <td>
                            <span
                              class="badge {workerBadge(channel.workerStatus)} badge-sm"
                              title={channel.workerStartedAt
                                ? `Started ${new Date(channel.workerStartedAt).toLocaleString()}`
                                : ''}
                            >
                              {channel.workerStatus}
                            </span>
                            {#if channel.workerStatus === 'running' && channel.workerStartedAt}
                              <div class="text-[10px] text-base-content/60 mt-0.5">
                                up {uptime(channel.workerStartedAt)}
                              </div>
                            {/if}
                            {#if channel.lastSourceFetchAt}
                              <div
                                class="text-[10px] {srcStatusColor(channel.lastSourceStatus)} mt-0.5"
                                title={`Last source check: ${new Date(channel.lastSourceFetchAt).toLocaleString()}`}
                              >
                                src {channel.lastSourceStatus ?? '—'} · {relTime(channel.lastSourceFetchAt)}
                              </div>
                            {/if}
                          </td>
                          <td class="w-[3.5rem]">
                            <button
                              class="btn btn-outline btn-xs btn-square"
                              type="button"
                              title={channel.playlistUrl ? `Copy: ${channel.playlistUrl}` : 'Not available yet'}
                              aria-label="Copy URL"
                              on:click={() => copy(channel.playlistUrl)}
                              disabled={!channel.playlistUrl}
                            >
                              📋
                            </button>
                          </td>
                          <td class="text-xs">
                            <div>{bytes(metrics[channel.id]?.bytesSent || 0)}</div>
                            <div class="text-base-content/60">
                              {metrics[channel.id]?.segmentRequests || 0} req
                            </div>
                            <div class="text-success">
                              👁 {metrics[channel.id]?.liveViewers || 0} live
                            </div>
                          </td>
                          <td>
                            <div class="flex flex-wrap justify-end gap-1">
                              <button
                                class="btn btn-ghost btn-xs btn-square"
                                type="button"
                                title="Preview in browser"
                                aria-label="Preview"
                                on:click={() => openPlayer(channel)}
                                disabled={!channel.playlistUrl}
                              >
                                ▶
                              </button>
                              <button
                                class="btn btn-ghost btn-xs btn-square"
                                type="button"
                                title="Edit channel"
                                aria-label="Edit"
                                on:click={() => editChannel(channel)}
                              >
                                ✏️
                              </button>
                              <button
                                class="btn btn-ghost btn-xs btn-square"
                                type="button"
                                title="Generate share link"
                                aria-label="Share"
                                on:click={() => openShare(channel)}
                              >
                                🔗
                              </button>
                              {#if channel.workerStatus !== 'running'}
                                <button
                                  class="btn btn-ghost btn-xs btn-square text-success"
                                  type="button"
                                  title="Start worker"
                                  aria-label="Start"
                                  on:click={() => workerAction(channel, 'start')}
                                >
                                  ⏵
                                </button>
                              {:else}
                                <button
                                  class="btn btn-ghost btn-xs btn-square text-warning"
                                  type="button"
                                  title="Stop worker"
                                  aria-label="Stop"
                                  on:click={() => workerAction(channel, 'stop')}
                                >
                                  ⏹
                                </button>
                              {/if}
                              <button
                                class="btn btn-ghost btn-xs btn-square"
                                type="button"
                                title="Purge cache"
                                aria-label="Purge"
                                on:click={() => purgeCache(channel)}
                              >
                                🧹
                              </button>
                              <button
                                class="btn btn-ghost btn-xs btn-square text-error"
                                type="button"
                                title="Delete channel"
                                aria-label="Delete"
                                on:click={() => removeChannel(channel)}
                              >
                                🗑️
                              </button>
                            </div>
                          </td>
                        </tr>
                      {/each}
                    {/if}
                  </tbody>
                </table>
              </div>
            </div>
          </div>
        </section>
    </div>
  {/if}

  {#if previewChannel}
    <div
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4"
      role="dialog"
      aria-modal="true"
    >
      <div class="card w-full max-w-3xl bg-base-100 shadow-xl">
        <div class="card-body">
          <div class="flex items-center justify-between gap-3">
            <div>
              <h3 class="card-title text-base">{previewChannel.name}</h3>
              <p class="text-xs text-base-content/60">{previewChannel.playlistUrl}</p>
            </div>
            <button class="btn btn-ghost btn-sm" type="button" on:click={closePlayer}>Close</button>
          </div>
          <div class="aspect-video w-full overflow-hidden rounded bg-black">
            <!-- svelte-ignore a11y-media-has-caption -->
            <video bind:this={videoEl} class="h-full w-full" controls autoplay muted></video>
          </div>
          <div class="flex flex-wrap gap-2">
            <button
              class="btn btn-outline btn-sm"
              type="button"
              on:click={() => copy(previewChannel?.playlistUrl ?? '')}
            >
              Copy URL
            </button>
            <a
              class="btn btn-outline btn-sm"
              href={previewChannel.playlistUrl}
              target="_blank"
              rel="noopener noreferrer"
            >
              Open m3u8
            </a>
          </div>
        </div>
      </div>
    </div>
  {/if}

  {#if showForm}
    <div
      class="fixed inset-0 z-40 flex items-start justify-center overflow-y-auto bg-black/70 p-4 py-10"
      role="dialog"
      aria-modal="true"
    >
      <div class="card w-full max-w-xl bg-base-100 shadow-xl">
        <div class="card-body">
            <div class="flex items-center justify-between">
              <h2 class="card-title text-base">{editingId ? 'Edit Channel' : 'New Channel'}</h2>
              <button class="btn btn-ghost btn-sm" type="button" on:click={resetForm}>Cancel</button>
            </div>

            <form class="mt-2 grid gap-3" on:submit|preventDefault={saveChannel}>
              <label class="form-control">
                <span class="label-text">Name</span>
                <input
                  class="input input-bordered"
                  bind:value={form.name}
                  on:blur={autoSlug}
                  required
                  placeholder="Channel 1"
                />
              </label>

              <label class="form-control">
                <span class="label-text">Slug</span>
                <input
                  class="input input-bordered"
                  bind:value={form.slug}
                  required
                  placeholder="channel-1"
                />
                <span class="label-text-alt mt-1 text-base-content/60">
                  Output URL: <code>{getApiBase()}/proxy/{form.slug || 'slug'}/index.m3u8</code>
                </span>
              </label>

              <label class="form-control">
                <span class="label-text">Source URL</span>
                <input
                  class="input input-bordered"
                  bind:value={form.inputUrl}
                  required
                  placeholder="http://provider.tld/play/TOKEN"
                />
              </label>

              <div class="grid gap-3 sm:grid-cols-2">
                <label class="form-control">
                  <span class="label-text">Mode</span>
                  <select class="select select-bordered" bind:value={form.mode}>
                    <option value="transmux">Transmux (FFmpeg • direct stream)</option>
                    <option value="ingest">Ingest (HLS source)</option>
                    <option value="proxy">Proxy (HLS on-demand)</option>
                  </select>
                </label>
                <label class="form-control">
                  <span class="label-text">Status</span>
                  <select class="select select-bordered" bind:value={form.status}>
                    <option value="active">Active</option>
                    <option value="disabled">Disabled</option>
                  </select>
                </label>
              </div>
              <span class="-mt-2 text-xs text-base-content/60">{MODE_DESC[form.mode]}</span>

              <div class="rounded-lg border border-base-300 p-3">
                <label class="label cursor-pointer justify-start gap-3 p-0">
                  <input
                    class="toggle toggle-primary toggle-sm"
                    bind:checked={form.syncEnabled}
                    type="checkbox"
                  />
                  <span class="label-text font-medium">Viewer sync</span>
                </label>
                <p class="mt-1 text-xs text-base-content/60">
                  When enabled, all viewers see exactly the same segments (uniform delay from live edge).
                </p>
                {#if form.syncEnabled}
                  <label class="form-control mt-2">
                    <span class="label-text text-xs">Delay (seconds)</span>
                    <input
                      class="input input-bordered input-sm"
                      bind:value={form.syncDelaySeconds}
                      min="6"
                      max="300"
                      type="number"
                    />
                    <span class="label-text-alt mt-1 text-base-content/60">
                      Recommended ≥ 20s to absorb viewer network jitter.
                    </span>
                  </label>
                {/if}
              </div>

              <button
                class="btn btn-ghost btn-sm justify-start"
                type="button"
                on:click={() => (showAdvanced = !showAdvanced)}
              >
                {showAdvanced ? '▾' : '▸'} Advanced options
              </button>

              {#if showAdvanced}
                <div class="grid grid-cols-3 gap-2">
                  <label class="form-control">
                    <span class="label-text text-xs">Playlist TTL</span>
                    <input
                      class="input input-bordered input-sm"
                      bind:value={form.playlistTtlSeconds}
                      min="1"
                      type="number"
                    />
                  </label>
                  <label class="form-control">
                    <span class="label-text text-xs">Segment TTL</span>
                    <input
                      class="input input-bordered input-sm"
                      bind:value={form.segmentTtlSeconds}
                      min="1"
                      type="number"
                    />
                  </label>
                  <label class="form-control">
                    <span class="label-text text-xs">Poll (s)</span>
                    <input
                      class="input input-bordered input-sm"
                      bind:value={form.ingestPollSeconds}
                      min="1"
                      type="number"
                    />
                  </label>
                </div>
                <label class="label cursor-pointer justify-start gap-3">
                  <input class="toggle toggle-primary toggle-sm" bind:checked={form.cacheEnabled} type="checkbox" />
                  <span class="label-text">Cache enabled (for ingest/proxy)</span>
                </label>
              {/if}

              <div class="mt-2 flex justify-end gap-2">
                <button class="btn btn-ghost" type="button" on:click={resetForm}>Cancel</button>
                <button class="btn btn-primary" disabled={saving} type="submit">
                  {saving ? 'Saving…' : editingId ? 'Save Changes' : 'Create Channel'}
                </button>
              </div>
            </form>
        </div>
      </div>
    </div>
  {/if}

  {#if shareChannel}
    <div
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4"
      role="dialog"
      aria-modal="true"
    >
      <div class="card w-full max-w-lg bg-base-100 shadow-xl">
        <div class="card-body">
          <div class="flex items-center justify-between gap-3">
            <h3 class="card-title text-base">Share — {shareChannel.name}</h3>
            <button class="btn btn-ghost btn-sm" type="button" on:click={closeShare}>Close</button>
          </div>
          <p class="text-xs text-base-content/60">
            Generate a signed playback link. Anyone with the URL can watch until it expires.
          </p>
          <label class="form-control mt-2">
            <span class="label-text">Link valid for</span>
            <select class="select select-bordered" bind:value={shareTtl}>
              <option value={60}>1 hour</option>
              <option value={240}>4 hours</option>
              <option value={720}>12 hours</option>
              <option value={1440}>1 day</option>
              <option value={10080}>1 week</option>
              <option value={43200}>30 days</option>
            </select>
          </label>
          <button class="btn btn-primary mt-2" type="button" disabled={shareLoading} on:click={generateShare}>
            {shareLoading ? 'Generating…' : 'Generate link'}
          </button>
          {#if shareResult}
            <div class="mt-3 grid gap-3 text-xs">
              <div>
                <div class="mb-1 font-medium">Direct .m3u8 (VLC / IPTV player)</div>
                <div class="flex gap-1">
                  <input
                    class="input input-bordered input-sm flex-1 font-mono text-xs"
                    readonly
                    value={shareResult.playlistUrl}
                  />
                  <button class="btn btn-sm" type="button" on:click={() => copy(shareResult?.playlistUrl ?? '')}>📋</button>
                </div>
              </div>
              <div>
                <div class="mb-1 font-medium">Embed page (browser / iframe)</div>
                <div class="flex gap-1">
                  <input
                    class="input input-bordered input-sm flex-1 font-mono text-xs"
                    readonly
                    value={shareResult.embedUrl}
                  />
                  <button class="btn btn-sm" type="button" on:click={() => copy(shareResult?.embedUrl ?? '')}>📋</button>
                  <a class="btn btn-sm" href={shareResult.embedUrl} target="_blank" rel="noopener noreferrer">Open</a>
                </div>
              </div>
              <div class="text-base-content/60">
                Expires at: {new Date(shareResult.expiresAt).toLocaleString()}
              </div>
            </div>
          {/if}
        </div>
      </div>
    </div>
  {/if}
</main>

<style>
  :global(html, body) {
    height: 100%;
  }
</style>
