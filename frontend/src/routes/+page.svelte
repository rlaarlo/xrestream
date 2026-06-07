<script lang="ts">
  import { onMount, onDestroy, tick } from 'svelte';
  import videojs from 'video.js';
  import type Player from 'video.js/dist/types/player';
  import 'video.js/dist/video-js.css';
  import Icon from '@iconify/svelte';
  import ThemeToggle from '$lib/ThemeToggle.svelte';
  import {
    apiFetch,
    getApiBase,
    getStoredToken,
    setStoredToken,
    createShareLink,
    mintPlaybackToken,
    listNodes,
    listUsers,
    fetchMe,
    type Channel,
    type User,
    type ChannelInput,
    type Metrics,
    type ShareLink,
    type Node,
    type AuthMe
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
  let nodes: Node[] = [];
  let users: User[] = [];
  let me: AuthMe | null = null;
  let loading = true;
  let saving = false;
  let loggingIn = false;
  let error = '';
  let notice = '';
  let username = '';
  let password = '';
  let authenticated = false;
  let showAdvanced = false;
  let authMode: 'login' | 'signup' = 'login';
  let allowSignup = true;

  let form: ChannelInput = blankForm();
  let editingId = '';
  let showForm = false;

  let previewChannel: Channel | null = null;
  let videoEl: HTMLVideoElement | null = null;
  let player: Player | null = null;
  let refreshTimer: ReturnType<typeof setInterval> | null = null;
  let noticeTimer: ReturnType<typeof setTimeout> | null = null;
  let errorTimer: ReturnType<typeof setTimeout> | null = null;
  const TOAST_DURATION = 4000;

  $: if (notice) {
    if (noticeTimer) clearTimeout(noticeTimer);
    noticeTimer = setTimeout(() => (notice = ''), TOAST_DURATION);
  }
  $: if (error) {
    if (errorTimer) clearTimeout(errorTimer);
    errorTimer = setTimeout(() => (error = ''), TOAST_DURATION);
  }

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
      syncDelaySeconds: 8,
      playbackTokenRequired: true,
      allowedOriginsBypass: false,
      httpReferer: '',
      httpUserAgent: '',
      httpOrigin: '',
      nodeId: null
    };
  }

  onMount(() => {
    authenticated = Boolean(getStoredToken());
    if (authenticated) {
      void loadChannels();
      refreshTimer = setInterval(() => void refreshMetrics(), 5000);
    } else {
      loading = false;
      void apiFetch<{ allowSignup: boolean }>('/auth/config')
        .then((c) => (allowSignup = c.allowSignup))
        .catch(() => {});
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
      const path = authMode === 'signup' ? '/auth/signup' : '/auth/login';
      const response = await apiFetch<{ token?: string; pendingReview?: boolean; message?: string }>(path, {
        method: 'POST',
        body: JSON.stringify({ username, password })
      });
      if (authMode === 'signup' && (!response.token || response.pendingReview)) {
        password = '';
        authMode = 'login';
        notice = response.message || 'Account created. Waiting for admin approval before you can sign in.';
        return;
      }
      setStoredToken(response.token!);
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
      try { nodes = await listNodes(); } catch { nodes = []; }
      try { me = await fetchMe(); } catch { me = null; }
      if (me?.role === 'admin') {
        try { users = await listUsers(); } catch { users = []; }
      } else {
        users = [];
      }
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
      syncDelaySeconds: channel.syncDelaySeconds,
      playbackTokenRequired: channel.playbackTokenRequired ?? true,
      allowedOriginsBypass: channel.allowedOriginsBypass ?? false,
      nodeId: channel.nodeId ?? null,
      httpReferer: channel.httpReferer ?? '',
      httpUserAgent: channel.httpUserAgent ?? '',
      httpOrigin: channel.httpOrigin ?? ''
    };
    showForm = true;
  }

  async function toggleSyncEnabled(channel: Channel) {
    const next = !channel.syncEnabled;
    // Optimistic UI: flip locally first.
    channels = channels.map((c) => (c.id === channel.id ? { ...c, syncEnabled: next } : c));
    try {
      await apiFetch<Channel>(`/channels/${channel.id}`, {
        method: 'PATCH',
        body: JSON.stringify({
          name: channel.name,
          slug: channel.slug,
          inputUrl: channel.inputUrl,
          mode: channel.mode,
          status: channel.status,
          playlistTtlSeconds: channel.playlistTtlSeconds,
          segmentTtlSeconds: channel.segmentTtlSeconds,
          ingestPollSeconds: channel.ingestPollSeconds,
          cacheEnabled: channel.cacheEnabled,
          syncEnabled: next,
          syncDelaySeconds: channel.syncDelaySeconds,
          playbackTokenRequired: channel.playbackTokenRequired ?? true,
          allowedOriginsBypass: channel.allowedOriginsBypass ?? false,
          httpReferer: channel.httpReferer ?? '',
          httpUserAgent: channel.httpUserAgent ?? '',
          httpOrigin: channel.httpOrigin ?? '',
          nodeId: channel.nodeId ?? null
        })
      });
      notice = `Storage for ${channel.name} → ${next ? 'R2' : 'VPS'}.`;
      await loadChannels();
    } catch (err) {
      // Revert on failure.
      channels = channels.map((c) => (c.id === channel.id ? { ...c, syncEnabled: !next } : c));
      error = messageFrom(err);
    }
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
    disposePlayer();
    let playbackUrl = channel.playlistUrl;
    try {
      const token = await mintPlaybackToken(channel.id);
      playbackUrl = token.playbackUrl;
    } catch (e) {
      error = `Failed to mint playback token: ${(e as Error).message}`;
      return;
    }
    player = videojs(videoEl, {
      controls: true,
      autoplay: true,
      muted: true,
      preload: 'auto',
      fluid: true,
      liveui: false,
      html5: {
        vhs: {
          overrideNative: true,
          enableLowInitialPlaylist: true,
          smoothQualityChange: true,
          handlePartialData: true
        },
        nativeAudioTracks: false,
        nativeVideoTracks: false
      }
    });
    player.src({ src: playbackUrl, type: 'application/x-mpegURL' });
  }

  function closePlayer() {
    previewChannel = null;
    disposePlayer();
  }

  function disposePlayer() {
    if (player) {
      player.dispose();
      player = null;
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
    if (worker === 'running') return 'badge-info';
    if (worker === 'error') return 'badge-error';
    return 'badge-ghost';
  }

  function messageFrom(err: unknown) {
    return err instanceof Error ? err.message : 'An error occurred.';
  }

  function handleKeydown(e: KeyboardEvent) {
    if (e.key.toLowerCase() === 'n' && !showForm && !previewChannel && !shareChannel && authenticated && (e.target as HTMLElement)?.tagName !== 'INPUT' && (e.target as HTMLElement)?.tagName !== 'TEXTAREA' && (e.target as HTMLElement)?.tagName !== 'SELECT') {
      e.preventDefault();
      openCreateForm();
    }
  }


  $: runningCount = channels.filter((c) => c.workerStatus === 'running').length;
  $: errorCount = channels.filter((c) => c.workerStatus === 'error').length;
  $: totalSent = Object.values(metrics).reduce((sum, m) => sum + (m?.bytesSent || 0), 0);
  $: totalViewers = Object.values(metrics).reduce((sum, m) => sum + (m?.liveViewers || 0), 0);

  let channelSearch = '';
  let modeFilter: 'all' | Mode = 'all';
  let workerFilter: 'all' | Channel['workerStatus'] = 'all';
  let ownerFilter: 'all' | 'mine' | 'others' = 'mine';
  let channelPage = 1;
  let channelPageSize = 10;
  $: isAdmin = me?.role === 'admin';
  $: ownerMap = new Map(users.map((u) => [u.id, u.username] as const));
  $: hasOthers = isAdmin && channels.some((c) => c.ownerId && c.ownerId !== me?.userId);
  $: mineCount = channels.filter((c) => !!me && c.ownerId === me.userId).length;
  $: othersCount = channels.filter((c) => !!me && c.ownerId && c.ownerId !== me.userId).length;
  $: visibleChannels = channels.filter((c) => {
    const q = channelSearch.trim().toLowerCase();
    if (q && !(c.name.toLowerCase().includes(q) || c.slug.toLowerCase().includes(q) || c.inputUrl.toLowerCase().includes(q))) return false;
    if (modeFilter !== 'all' && c.mode !== modeFilter) return false;
    if (workerFilter !== 'all' && c.workerStatus !== workerFilter) return false;
    if (hasOthers && me) {
      if (ownerFilter === 'mine' && c.ownerId !== me.userId) return false;
      if (ownerFilter === 'others' && (!c.ownerId || c.ownerId === me.userId)) return false;
    }
    return true;
  });
  $: channelTotalPages = Math.max(1, Math.ceil(visibleChannels.length / channelPageSize));
  $: if (channelPage > channelTotalPages) channelPage = channelTotalPages;
  $: {
    channelSearch; modeFilter; workerFilter; ownerFilter; channelPageSize;
    channelPage = 1;
  }
  $: pagedChannels = visibleChannels.slice((channelPage - 1) * channelPageSize, channelPage * channelPageSize);
  $: channelRangeStart = visibleChannels.length === 0 ? 0 : (channelPage - 1) * channelPageSize + 1;
  $: channelRangeEnd = Math.min(visibleChannels.length, channelPage * channelPageSize);
</script>

<svelte:head>
  <title>Restream Dashboard</title>
</svelte:head>

<svelte:window on:keydown={handleKeydown} />

{#if notice || error}
  <div class="toast toast-top toast-center z-[100] w-full max-w-md px-4">
    {#if error}
      <div class="alert alert-error shadow-lg">
        <Icon icon="line-md:close-circle" class="text-lg" />
        <span class="flex-1 text-sm">{error}</span>
        <button class="btn btn-ghost btn-xs btn-square" type="button" aria-label="Dismiss" on:click={() => (error = '')}>
          <Icon icon="line-md:close" class="text-base" />
        </button>
      </div>
    {/if}
    {#if notice}
      <div class="alert alert-success shadow-lg">
        <Icon icon="line-md:confirm-circle" class="text-lg" />
        <span class="flex-1 text-sm">{notice}</span>
        <button class="btn btn-ghost btn-xs btn-square" type="button" aria-label="Dismiss" on:click={() => (notice = '')}>
          <Icon icon="line-md:close" class="text-base" />
        </button>
      </div>
    {/if}
  </div>
{/if}

<main class="min-h-screen bg-base-200 text-base-content">
  {#if !authenticated}
    <div class="flex min-h-screen items-center justify-center px-4 py-8">
      <div class="w-full max-w-sm">
        <div class="mb-6 flex items-center justify-between">
          <div class="flex items-center gap-2">
            <span class="grid h-9 w-9 place-items-center rounded-xl bg-primary text-primary-content shadow-sm">
              <Icon icon="lucide:radio-tower" class="text-lg" />
            </span>
            <div>
              <div class="text-base font-semibold leading-tight">Restream</div>
              <div class="text-[11px] uppercase tracking-wider text-base-content/50">Control plane</div>
            </div>
          </div>
          <ThemeToggle />
        </div>
        <div class="surface p-6 shadow-soft">
          <h1 class="text-lg font-semibold">
            {authMode === 'signup' ? 'Create your account' : 'Sign in to your dashboard'}
          </h1>
          <p class="mt-1 text-sm text-base-content/60">
            {authMode === 'signup' ? 'New accounts require admin approval before you can sign in.' : 'Manage HLS channels, workers, and viewers.'}
          </p>

          {#if error}
            <div class="alert alert-error mt-4 text-sm">{error}</div>
          {/if}

          <form class="mt-4 grid gap-3" on:submit|preventDefault={login}>
            <label class="form-control">
              <span class="label-text">Username</span>
              <input class="input input-bordered" bind:value={username} placeholder="username" autocomplete="username" required />
            </label>
            <label class="form-control">
              <span class="label-text">Password</span>
              <input
                class="input input-bordered"
                bind:value={password}
                autocomplete={authMode === 'signup' ? 'new-password' : 'current-password'}
                required
                minlength={authMode === 'signup' ? 8 : undefined}
                type="password"
              />
              {#if authMode === 'signup'}
                <span class="label-text-alt mt-1 text-base-content/60">Min 8 characters.</span>
              {/if}
            </label>
            <button class="btn btn-primary mt-1" disabled={loggingIn} type="submit">
              {loggingIn ? (authMode === 'signup' ? 'Creating…' : 'Signing in…') : (authMode === 'signup' ? 'Create account' : 'Sign in')}
            </button>
          </form>
          {#if allowSignup}
            <div class="mt-4 text-center text-sm text-base-content/70">
              {#if authMode === 'login'}
                Don't have an account?
                <button type="button" class="link link-primary" on:click={() => { authMode = 'signup'; error = ''; }}>Sign up</button>
              {:else}
                Already have an account?
                <button type="button" class="link link-primary" on:click={() => { authMode = 'login'; error = ''; }}>Sign in</button>
              {/if}
            </div>
          {/if}
        </div>
        <p class="mt-4 text-center text-[11px] text-base-content/50">API endpoint: <span class="font-mono">{getApiBase()}</span></p>
      </div>
    </div>
  {:else}
    <header class="sticky top-0 z-30 border-b border-base-300 bg-base-100/85 backdrop-blur supports-[backdrop-filter]:bg-base-100/70">
      <div class="mx-auto flex max-w-7xl flex-col gap-3 px-4 py-3 sm:flex-row sm:items-center sm:justify-between">
        <div class="flex min-w-0 items-center gap-3">
          <span class="grid h-9 w-9 shrink-0 place-items-center rounded-xl bg-primary text-primary-content shadow-sm">
            <Icon icon="lucide:radio-tower" class="text-lg" />
          </span>
          <div class="min-w-0">
            <div class="flex items-center gap-2">
              <h1 class="truncate text-base font-semibold">Restream Control</h1>
              {#if errorCount > 0}
                <span class="badge badge-error badge-sm gap-1 font-medium">
                  <span class="status-dot"></span>{errorCount} error
                </span>
              {:else if runningCount > 0}
                <span class="badge badge-success badge-sm gap-1 font-medium">
                  <span class="status-dot"></span>{runningCount} live
                </span>
              {:else}
                <span class="badge badge-ghost badge-sm">idle</span>
              {/if}
            </div>
            <p class="truncate text-[11px] text-base-content/55">HLS relay without re-encode · .m3u8 output for any player</p>
          </div>
        </div>
        <div class="flex flex-wrap items-center gap-1.5">
          <span class="hidden items-center gap-1 rounded-md border border-base-300 px-2 py-1 text-[11px] text-base-content/60 md:inline-flex" title="Backend API endpoint">
            <Icon icon="lucide:server" class="text-sm" />
            <span class="font-mono">{getApiBase()}</span>
          </span>
          <button class="btn btn-ghost btn-sm gap-1" type="button" on:click={loadChannels} title="Refresh">
            <Icon icon="lucide:refresh-cw" class="text-base" />
            <span class="hidden sm:inline">Refresh</span>
          </button>
          <a class="btn btn-ghost btn-sm gap-1" href="/settings">
            <Icon icon="lucide:settings" class="text-base" />
            <span class="hidden sm:inline">Settings</span>
          </a>
          <ThemeToggle />
          <div class="dropdown dropdown-end">
            <button type="button" class="btn btn-ghost btn-sm gap-2" aria-label="Account">
              <span class="grid h-6 w-6 place-items-center rounded-full bg-primary/15 text-[11px] font-bold text-primary">
                {(me?.username || username || '?').charAt(0).toUpperCase()}
              </span>
              <span class="hidden text-sm font-medium sm:inline">{me?.username || username}</span>
              <Icon icon="lucide:chevron-down" class="text-sm opacity-60" />
            </button>
            <ul class="dropdown-content menu menu-sm z-50 mt-1 w-48 rounded-box border border-base-300 bg-base-100 p-1.5 shadow-lg">
              <li class="px-2 pb-1 pt-1 text-[11px] uppercase tracking-wider text-base-content/50">Signed in as</li>
              <li class="px-2 pb-2 text-sm font-medium">{me?.username || username} · <span class="text-base-content/60">{me?.role || ''}</span></li>
              <li><a href="/settings" class="flex items-center gap-2"><Icon icon="lucide:settings" /> Settings</a></li>
              <li><button type="button" on:click={logout} class="flex items-center gap-2 text-error"><Icon icon="lucide:log-out" /> Logout</button></li>
            </ul>
          </div>
        </div>
      </div>
    </header>

    <div class="mx-auto max-w-7xl px-4 py-6">
      <div class="w-full">
        <div class="mb-6 grid gap-3 grid-cols-2 md:grid-cols-4">
          <div class="surface flex items-center gap-3 p-4 shadow-soft">
            <span class="grid h-10 w-10 shrink-0 place-items-center rounded-lg bg-primary/10 text-primary">
              <Icon icon="lucide:radio" class="text-xl" />
            </span>
            <div class="min-w-0">
              <div class="text-2xl font-bold leading-none">{channels.length}</div>
              <div class="mt-1.5 text-[11px] font-medium uppercase tracking-wide text-base-content/55">Channels</div>
            </div>
          </div>
          <div class="surface flex items-center gap-3 p-4 shadow-soft">
            <span class="grid h-10 w-10 shrink-0 place-items-center rounded-lg bg-success/10 text-success">
              <Icon icon="lucide:activity" class="text-xl" />
            </span>
            <div class="min-w-0">
              <div class="text-2xl font-bold leading-none text-success">{runningCount}</div>
              <div class="mt-1.5 text-[11px] font-medium uppercase tracking-wide text-base-content/55">Workers running</div>
            </div>
          </div>
          <div class="surface flex items-center gap-3 p-4 shadow-soft">
            <span class="grid h-10 w-10 shrink-0 place-items-center rounded-lg {errorCount > 0 ? 'bg-error/10 text-error' : 'bg-base-200 text-base-content/40'}">
              <Icon icon="lucide:triangle-alert" class="text-xl" />
            </span>
            <div class="min-w-0">
              <div class="text-2xl font-bold leading-none {errorCount > 0 ? 'text-error' : ''}">{errorCount}</div>
              <div class="mt-1.5 text-[11px] font-medium uppercase tracking-wide text-base-content/55">Worker errors</div>
            </div>
          </div>
          <div class="surface flex items-center gap-3 p-4 shadow-soft">
            <span class="grid h-10 w-10 shrink-0 place-items-center rounded-lg bg-info/10 text-info">
              <Icon icon="lucide:cloud-upload" class="text-xl" />
            </span>
            <div class="min-w-0">
              <div class="text-2xl font-bold leading-none">{bytes(totalSent)}</div>
              <div class="mt-1.5 text-[11px] font-medium uppercase tracking-wide text-base-content/55">Bytes sent (1h)</div>
            </div>
          </div>
        </div>

      <div class="mb-3 flex flex-col gap-3 md:flex-row md:items-end md:justify-between">
        <div>
          <h2 class="text-lg font-semibold">Channels</h2>
          <p class="text-xs text-base-content/55">
            {visibleChannels.length} shown · {runningCount} running · {totalViewers} live viewers
          </p>
        </div>
        <div class="flex flex-wrap items-center gap-1.5">
          {#if hasOthers}
            <select
              class="select select-bordered select-sm"
              bind:value={ownerFilter}
              title="Filter by owner"
              aria-label="Filter channels by owner"
            >
              <option value="mine">Mine ({mineCount})</option>
              <option value="all">All ({channels.length})</option>
              <option value="others">Users ({othersCount})</option>
            </select>
          {/if}
          <label class="input input-bordered input-sm flex items-center gap-2 w-full sm:w-64">
            <Icon icon="lucide:search" class="text-sm opacity-60" />
            <input type="search" class="grow text-sm" placeholder="Search name, slug, URL" bind:value={channelSearch} />
            {#if channelSearch}
              <button type="button" class="opacity-60 hover:opacity-100" aria-label="Clear" on:click={() => (channelSearch = '')}>
                <Icon icon="lucide:x" class="text-sm" />
              </button>
            {/if}
          </label>
          <select class="select select-bordered select-sm" bind:value={modeFilter} title="Filter by mode">
            <option value="all">All modes</option>
            <option value="transmux">Transmux</option>
            <option value="ingest">Ingest</option>
            <option value="proxy">Proxy</option>
          </select>
          <select class="select select-bordered select-sm" bind:value={workerFilter} title="Filter by worker">
            <option value="all">All workers</option>
            <option value="running">Running</option>
            <option value="stopped">Stopped</option>
            <option value="error">Error</option>
          </select>
          <button class="btn btn-primary btn-sm gap-1.5 shadow-soft" type="button" on:click={openCreateForm} title="Shortcut: N">
            <Icon icon="lucide:plus" class="text-base" />
            New Channel
            <kbd class="kbd kbd-xs ml-1 hidden lg:inline-flex">N</kbd>
          </button>
        </div>
      </div>

      <section class="min-w-0">
        <!-- Desktop: data table -->
        <div class="hidden rounded-xl border border-base-300 bg-base-100 shadow-soft md:block">
          <table class="table">
            <thead>
              <tr class="border-b border-base-300 bg-base-200/60 text-[11px] font-semibold uppercase tracking-wide text-base-content/60">
                <th class="py-3">Channel</th>
                <th class="py-3">Mode</th>
                <th class="py-3">Status</th>
                <th class="py-3">Worker</th>
                <th class="py-3">Storage</th>
                <th class="py-3">Traffic</th>
                <th class="py-3">Viewers</th>
                <th class="py-3 text-right">Actions</th>
              </tr>
            </thead>
            <tbody>
              {#if loading}
                <tr>
                  <td colspan="8" class="py-12 text-center text-sm text-base-content/60">
                    <span class="loading loading-spinner loading-sm align-middle"></span>
                    <span class="ml-2 align-middle">Loading channels…</span>
                  </td>
                </tr>
              {:else if channels.length === 0}
                <tr>
                  <td colspan="8" class="py-16 text-center">
                    <div class="mx-auto flex max-w-sm flex-col items-center gap-3">
                      <span class="grid h-12 w-12 place-items-center rounded-full bg-primary/10 text-primary">
                        <Icon icon="lucide:radio-tower" class="text-2xl" />
                      </span>
                      <div>
                        <div class="text-sm font-semibold">No channels yet</div>
                        <div class="mt-1 text-xs text-base-content/55">
                          Create your first restream channel to start relaying an HLS source.
                        </div>
                      </div>
                      <button class="btn btn-primary btn-sm gap-1.5" type="button" on:click={openCreateForm}>
                        <Icon icon="lucide:plus" class="text-base" /> New Channel
                      </button>
                    </div>
                  </td>
                </tr>
              {:else if visibleChannels.length === 0}
                <tr>
                  <td colspan="8" class="py-12 text-center text-sm text-base-content/60">
                    No channels match these filters.
                    <button type="button" class="link link-primary ml-1" on:click={() => { channelSearch = ''; modeFilter = 'all'; workerFilter = 'all'; }}>Reset</button>
                  </td>
                </tr>
              {:else}
                {#each pagedChannels as channel (channel.id)}
                  <tr class="border-b border-base-200 align-middle transition-colors last:border-b-0 hover:bg-base-200/40">
                    <td class="py-3.5">
                      <div class="min-w-0 max-w-[18rem]">
                        <div class="truncate text-sm font-semibold" title={channel.name}>{channel.name}</div>
                        <div class="truncate text-xs text-base-content/50" title={channel.slug}>/{channel.slug}</div>
                        {#if isAdmin && channel.ownerId}
                          <div class="mt-1 flex items-center gap-1 text-[11px] text-base-content/60" title={channel.ownerId}>
                            <Icon icon="lucide:user" class="shrink-0 text-xs" />
                            <span class="truncate">{ownerMap.get(channel.ownerId) || channel.ownerId.slice(0, 8)}</span>
                            {#if channel.ownerId === me?.userId}
                              <span class="badge badge-ghost badge-xs">you</span>
                            {/if}
                          </div>
                        {/if}
                        {#if channel.lastError}
                          <div class="mt-1 flex items-center gap-1 text-xs font-medium text-error" title={channel.lastError}>
                            <Icon icon="line-md:bell-alert-twotone" class="shrink-0 text-sm" />
                            <span class="truncate">{channel.lastError}</span>
                          </div>
                        {/if}
                      </div>
                    </td>
                    <td class="py-3.5">
                      <span class="badge {modeBadge(channel.mode)} badge-sm whitespace-nowrap">
                        {MODE_LABEL[channel.mode]}
                      </span>
                    </td>
                    <td class="py-3.5">
                      <span class="badge {statusBadge(channel.status)} badge-sm whitespace-nowrap">{channel.status}</span>
                    </td>
                    <td class="py-3.5">
                      <div class="flex flex-col gap-0.5">
                        <span class="badge {workerBadge(channel.workerStatus)} badge-sm w-fit whitespace-nowrap">
                          {channel.workerStatus}
                        </span>
                        {#if channel.workerStatus === 'running' && channel.workerStartedAt}
                          <span class="text-[11px] text-base-content/50">up {uptime(channel.workerStartedAt)}</span>
                        {/if}
                        {#if channel.lastSourceFetchAt}
                          <span
                            class="text-[11px] {srcStatusColor(channel.lastSourceStatus)}"
                            title={`Last source check: ${new Date(channel.lastSourceFetchAt).toLocaleString()}`}
                          >
                            src {channel.lastSourceStatus ?? '—'} · {relTime(channel.lastSourceFetchAt)}
                          </span>
                        {/if}
                      </div>
                    </td>
                    <td class="py-3.5">
                      <label class="flex w-fit cursor-pointer items-center gap-2" title="Toggle Cloudflare R2 Sync">
                        <input
                          type="checkbox"
                          class="toggle toggle-sm {channel.syncEnabled ? 'toggle-warning' : ''}"
                          checked={channel.syncEnabled}
                          on:change={() => toggleSyncEnabled(channel)}
                        />
                        <span class="text-xs font-bold {channel.syncEnabled ? 'text-warning' : 'text-base-content/50'}">
                          {channel.syncEnabled ? 'R2' : 'VPS'}
                        </span>
                      </label>
                    </td>
                    <td class="py-3.5">
                      <div class="flex flex-col gap-0.5">
                        {#if channel.syncEnabled}
                          <span class="text-sm font-medium text-warning">Via R2</span>
                          <span class="text-[11px] text-base-content/45">VPS: {bytes(metrics[channel.id]?.bytesSent || 0)}</span>
                        {:else}
                          <span class="text-sm font-medium">{bytes(metrics[channel.id]?.bytesSent || 0)}</span>
                        {/if}
                        <span class="text-[11px] text-base-content/50">{metrics[channel.id]?.segmentRequests || 0} req</span>
                      </div>
                    </td>
                    <td class="py-3.5">
                      <span class="inline-flex items-center gap-1 text-sm font-medium text-success">
                        <Icon icon="line-md:watch" class="text-base" />
                        {metrics[channel.id]?.liveViewers || 0}
                      </span>
                    </td>
                    <td class="py-3.5 text-right">
                      <div class="flex items-center justify-end gap-0.5">
                        <button
                          class="btn btn-ghost btn-sm btn-square"
                          type="button"
                          title={channel.playlistUrl ? `Copy: ${channel.playlistUrl}` : 'Not available yet'}
                          aria-label="Copy URL"
                          on:click={() => copy(channel.playlistUrl)}
                          disabled={!channel.playlistUrl}
                        >
                          <Icon icon="line-md:clipboard-list" class="text-lg" />
                        </button>
                        <div class="dropdown dropdown-end">
                          <button type="button" class="btn btn-ghost btn-sm btn-square" aria-label="Actions">
                            <Icon icon="line-md:menu" class="text-xl" />
                          </button>
                          <ul class="dropdown-content menu menu-sm z-[40] mt-1 w-44 rounded-box border border-base-300 bg-base-100 p-1.5 shadow-lg">
                            <li>
                              <button class="flex items-center gap-2" on:click={() => openPlayer(channel)} disabled={!channel.playlistUrl}>
                                <Icon icon="line-md:play" class="text-base" /> Preview
                              </button>
                            </li>
                            <li>
                              <button class="flex items-center gap-2" on:click={() => editChannel(channel)}>
                                <Icon icon="line-md:edit" class="text-base" /> Edit
                              </button>
                            </li>
                            <li>
                              <button class="flex items-center gap-2" on:click={() => openShare(channel)}>
                                <Icon icon="line-md:link" class="text-base" /> Share link
                              </button>
                            </li>
                            <li class="menu-title my-1 h-px bg-base-200 p-0"></li>
                            {#if channel.workerStatus !== 'running'}
                              <li>
                                <button class="flex items-center gap-2 text-success" on:click={() => workerAction(channel, 'start')}>
                                  <Icon icon="line-md:play-filled" class="text-base" /> Start Worker
                                </button>
                              </li>
                            {:else}
                              <li>
                                <button class="flex items-center gap-2 text-error" on:click={() => workerAction(channel, 'stop')}>
                                  <Icon icon="line-md:square" class="text-base" /> Stop Worker
                                </button>
                              </li>
                            {/if}
                            <li>
                              <button class="flex items-center gap-2" on:click={() => purgeCache(channel)}>
                                <Icon icon="line-md:rotate-270" class="text-base" /> Purge cache
                              </button>
                            </li>
                            <li class="menu-title my-1 h-px bg-base-200 p-0"></li>
                            <li>
                              <button class="flex items-center gap-2 font-medium text-error" on:click={() => removeChannel(channel)}>
                                <Icon icon="line-md:trash" class="text-base" /> Delete
                              </button>
                            </li>
                          </ul>
                        </div>
                      </div>
                    </td>
                  </tr>
                {/each}
              {/if}
            </tbody>
          </table>
        </div>

        <!-- Mobile: card list -->
        <div class="flex flex-col gap-3 md:hidden">
          {#if loading}
            <div class="surface p-6 text-center text-sm text-base-content/60 shadow-soft">
              <span class="loading loading-spinner loading-sm align-middle"></span>
              <span class="ml-2 align-middle">Loading channels…</span>
            </div>
          {:else if channels.length === 0}
            <div class="surface flex flex-col items-center gap-3 p-6 text-center shadow-soft">
              <span class="grid h-12 w-12 place-items-center rounded-full bg-primary/10 text-primary">
                <Icon icon="lucide:radio-tower" class="text-2xl" />
              </span>
              <div class="text-sm font-semibold">No channels yet</div>
              <div class="text-xs text-base-content/55">Create your first restream channel to start relaying an HLS source.</div>
              <button class="btn btn-primary btn-sm gap-1.5" type="button" on:click={openCreateForm}>
                <Icon icon="lucide:plus" class="text-base" /> New Channel
              </button>
            </div>
          {:else if visibleChannels.length === 0}
            <div class="surface p-6 text-center text-sm text-base-content/60 shadow-soft">
              No channels match these filters.
              <button type="button" class="link link-primary ml-1" on:click={() => { channelSearch = ''; modeFilter = 'all'; workerFilter = 'all'; }}>Reset</button>
            </div>
          {:else}
            {#each pagedChannels as channel (channel.id)}
              <div class="rounded-xl border border-base-300 bg-base-100 p-4 shadow-sm">
                <div class="flex items-start justify-between gap-2">
                  <div class="min-w-0 flex-1">
                    <div class="truncate text-sm font-semibold" title={channel.name}>{channel.name}</div>
                    <div class="truncate text-xs text-base-content/50">/{channel.slug}</div>
                    {#if isAdmin && channel.ownerId}
                      <div class="mt-1 flex items-center gap-1 text-[11px] text-base-content/60" title={channel.ownerId}>
                        <Icon icon="lucide:user" class="shrink-0 text-xs" />
                        <span class="truncate">{ownerMap.get(channel.ownerId) || channel.ownerId.slice(0, 8)}</span>
                        {#if channel.ownerId === me?.userId}
                          <span class="badge badge-ghost badge-xs">you</span>
                        {/if}
                      </div>
                    {/if}
                  </div>
                  <div class="flex items-center gap-1">
                    <button
                      class="btn btn-ghost btn-sm btn-square"
                      type="button"
                      aria-label="Copy URL"
                      on:click={() => copy(channel.playlistUrl)}
                      disabled={!channel.playlistUrl}
                    >
                      <Icon icon="line-md:clipboard-list" class="text-lg" />
                    </button>
                    <div class="dropdown dropdown-end">
                      <button type="button" class="btn btn-ghost btn-sm btn-square" aria-label="Actions">
                        <Icon icon="line-md:menu" class="text-xl" />
                      </button>
                      <ul class="dropdown-content menu menu-sm z-[40] mt-1 w-44 rounded-box border border-base-300 bg-base-100 p-1.5 shadow-lg">
                        <li>
                          <button class="flex items-center gap-2" on:click={() => openPlayer(channel)} disabled={!channel.playlistUrl}>
                            <Icon icon="line-md:play" class="text-base" /> Preview
                          </button>
                        </li>
                        <li>
                          <button class="flex items-center gap-2" on:click={() => editChannel(channel)}>
                            <Icon icon="line-md:edit" class="text-base" /> Edit
                          </button>
                        </li>
                        <li>
                          <button class="flex items-center gap-2" on:click={() => openShare(channel)}>
                            <Icon icon="line-md:link" class="text-base" /> Share link
                          </button>
                        </li>
                        <li class="menu-title my-1 h-px bg-base-200 p-0"></li>
                        {#if channel.workerStatus !== 'running'}
                          <li>
                            <button class="flex items-center gap-2 text-success" on:click={() => workerAction(channel, 'start')}>
                              <Icon icon="line-md:play-filled" class="text-base" /> Start Worker
                            </button>
                          </li>
                        {:else}
                          <li>
                            <button class="flex items-center gap-2 text-error" on:click={() => workerAction(channel, 'stop')}>
                              <Icon icon="line-md:square" class="text-base" /> Stop Worker
                            </button>
                          </li>
                        {/if}
                        <li>
                          <button class="flex items-center gap-2" on:click={() => purgeCache(channel)}>
                            <Icon icon="line-md:rotate-270" class="text-base" /> Purge cache
                          </button>
                        </li>
                        <li class="menu-title my-1 h-px bg-base-200 p-0"></li>
                        <li>
                          <button class="flex items-center gap-2 font-medium text-error" on:click={() => removeChannel(channel)}>
                            <Icon icon="line-md:trash" class="text-base" /> Delete
                          </button>
                        </li>
                      </ul>
                    </div>
                  </div>
                </div>

                <div class="mt-3 flex flex-wrap items-center gap-1.5">
                  <span class="badge {modeBadge(channel.mode)} badge-sm">{MODE_LABEL[channel.mode]}</span>
                  <span class="badge {statusBadge(channel.status)} badge-sm">{channel.status}</span>
                  <span class="badge {workerBadge(channel.workerStatus)} badge-sm">{channel.workerStatus}</span>
                  {#if channel.workerStatus === 'running' && channel.workerStartedAt}
                    <span class="text-[11px] text-base-content/50">up {uptime(channel.workerStartedAt)}</span>
                  {/if}
                </div>

                <div class="mt-3 grid grid-cols-2 gap-2 text-xs">
                  <div class="rounded-lg bg-base-200/50 p-2">
                    <div class="text-[10px] uppercase tracking-wide text-base-content/50">Traffic</div>
                    <div class="mt-0.5 font-medium">
                      {#if channel.syncEnabled}
                        <span class="text-warning">Via R2</span>
                      {:else}
                        {bytes(metrics[channel.id]?.bytesSent || 0)}
                      {/if}
                    </div>
                    <div class="text-[11px] text-base-content/50">{metrics[channel.id]?.segmentRequests || 0} req</div>
                  </div>
                  <div class="rounded-lg bg-base-200/50 p-2">
                    <div class="text-[10px] uppercase tracking-wide text-base-content/50">Viewers</div>
                    <div class="mt-0.5 flex items-center gap-1 font-medium text-success">
                      <Icon icon="line-md:watch" class="text-base" />
                      {metrics[channel.id]?.liveViewers || 0} live
                    </div>
                  </div>
                </div>

                <div class="mt-3 flex items-center justify-between border-t border-base-200 pt-3">
                  <span class="text-xs font-medium text-base-content/60">Storage</span>
                  <label class="flex cursor-pointer items-center gap-2">
                    <input
                      type="checkbox"
                      class="toggle toggle-sm {channel.syncEnabled ? 'toggle-warning' : ''}"
                      checked={channel.syncEnabled}
                      on:change={() => toggleSyncEnabled(channel)}
                    />
                    <span class="text-xs font-bold {channel.syncEnabled ? 'text-warning' : 'text-base-content/50'}">
                      {channel.syncEnabled ? 'R2' : 'VPS'}
                    </span>
                  </label>
                </div>

                {#if channel.lastError}
                  <div class="mt-2 flex items-center gap-1 text-xs font-medium text-error" title={channel.lastError}>
                    <Icon icon="line-md:bell-alert-twotone" class="shrink-0 text-sm" />
                    <span class="truncate">{channel.lastError}</span>
                  </div>
                {/if}
              </div>
            {/each}
          {/if}
        </div>

        {#if !loading && visibleChannels.length > 0}
          <div class="mt-3 flex flex-col items-center justify-between gap-2 sm:flex-row">
            <div class="text-xs text-base-content/55">
              Showing <span class="font-medium text-base-content/80">{channelRangeStart}–{channelRangeEnd}</span>
              of <span class="font-medium text-base-content/80">{visibleChannels.length}</span>
              {visibleChannels.length === channels.length ? '' : `(of ${channels.length} total)`}
            </div>
            <div class="flex items-center gap-2">
              <label class="flex items-center gap-1.5 text-xs text-base-content/55">
                <span class="hidden sm:inline">Rows</span>
                <select class="select select-bordered select-xs" bind:value={channelPageSize}>
                  <option value={10}>10</option>
                  <option value={25}>25</option>
                  <option value={50}>50</option>
                  <option value={100}>100</option>
                </select>
              </label>
              <div class="join">
                <button class="join-item btn btn-sm" type="button" aria-label="First page" on:click={() => (channelPage = 1)} disabled={channelPage <= 1}>
                  <Icon icon="lucide:chevrons-left" class="text-base" />
                </button>
                <button class="join-item btn btn-sm" type="button" aria-label="Previous page" on:click={() => (channelPage = Math.max(1, channelPage - 1))} disabled={channelPage <= 1}>
                  <Icon icon="lucide:chevron-left" class="text-base" />
                </button>
                <button class="join-item btn btn-sm btn-ghost no-animation pointer-events-none" type="button" tabindex="-1">
                  Page {channelPage} / {channelTotalPages}
                </button>
                <button class="join-item btn btn-sm" type="button" aria-label="Next page" on:click={() => (channelPage = Math.min(channelTotalPages, channelPage + 1))} disabled={channelPage >= channelTotalPages}>
                  <Icon icon="lucide:chevron-right" class="text-base" />
                </button>
                <button class="join-item btn btn-sm" type="button" aria-label="Last page" on:click={() => (channelPage = channelTotalPages)} disabled={channelPage >= channelTotalPages}>
                  <Icon icon="lucide:chevrons-right" class="text-base" />
                </button>
              </div>
            </div>
          </div>
        {/if}
      </section>
      </div> <!-- close w-full -->
    </div> <!-- close container flex -->
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
            <video
              bind:this={videoEl}
              class="video-js vjs-default-skin vjs-big-play-centered h-full w-full"
              playsinline
            ></video>
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
      <div class="card w-full max-w-4xl bg-base-100 shadow-xl">
        <div class="card-body">
            <div class="flex items-center justify-between">
              <h2 class="card-title text-base">{editingId ? 'Edit Channel' : 'New Channel'}</h2>
              <button class="btn btn-ghost btn-sm" type="button" on:click={resetForm}>Cancel</button>
            </div>

            <form class="mt-2 grid gap-3" on:submit|preventDefault={saveChannel}>
              <div class="grid gap-3 md:grid-cols-2">
                <label class="form-control">
                  <span class="label-text">Name</span>
                  <input
                    class="input input-bordered"
                    bind:value={form.name}
                    on:blur={autoSlug}
                    required
                    placeholder="Channel 1"
                  />
                  <span class="label-text-alt mt-1 text-base-content/60">
                    Display name shown in dashboard. Slug auto-generated.
                  </span>
                </label>

                <label class="form-control">
                  <span class="label-text">Slug</span>
                  <input
                    class="input input-bordered"
                    bind:value={form.slug}
                    required
                    placeholder="channel-1"
                  />
                  <span class="label-text-alt mt-1 truncate text-base-content/60" title="{getApiBase()}/proxy/{form.slug || 'slug'}/index.m3u8">
                    URL: <code>/proxy/{form.slug || 'slug'}/index.m3u8</code>
                  </span>
                </label>
              </div>

              <label class="form-control">
                <span class="label-text">Source URL</span>
                <input
                  class="input input-bordered"
                  bind:value={form.inputUrl}
                  required
                  placeholder="http://provider.tld/play/TOKEN"
                />
                <span class="label-text-alt mt-1 text-base-content/60">
                  Origin stream. <b>Transmux</b>: raw TS / MPEG-TS / any ffmpeg-readable URL.
                  <b>Ingest</b> / <b>Proxy</b>: must be HLS (<code>.m3u8</code>).
                </span>
              </label>

              <div class="rounded-lg border border-base-300 p-3">
                <div class="mb-2 text-sm font-medium">Upstream HTTP headers <span class="text-xs font-normal text-base-content/60">(optional)</span></div>
                <div class="grid gap-3 md:grid-cols-3">
                  <label class="form-control">
                    <span class="label-text text-xs">Referer</span>
                    <input
                      class="input input-bordered input-sm"
                      bind:value={form.httpReferer}
                      placeholder="https://example.com/"
                    />
                  </label>
                  <label class="form-control">
                    <span class="label-text text-xs">User-Agent</span>
                    <input
                      class="input input-bordered input-sm"
                      bind:value={form.httpUserAgent}
                      placeholder="VLC/3.0.20 LibVLC/3.0.20"
                    />
                  </label>
                  <label class="form-control">
                    <span class="label-text text-xs">Origin</span>
                    <input
                      class="input input-bordered input-sm"
                      bind:value={form.httpOrigin}
                      placeholder="https://example.com"
                    />
                  </label>
                </div>
                <p class="mt-2 text-xs text-base-content/60">
                  Dikirim saat backend menarik source upstream (proxy/ingest fetch dan ffmpeg transmux).
                  Kosongkan untuk pakai default.
                </p>
              </div>

              <div class="grid gap-3 md:grid-cols-2">
                <label class="form-control">
                  <span class="label-text">Mode</span>
                  <select class="select select-bordered" bind:value={form.mode}>
                    <option value="transmux">Transmux (FFmpeg • direct stream)</option>
                    <option value="ingest">Ingest (HLS source)</option>
                    <option value="proxy">Proxy (HLS on-demand)</option>
                  </select>
                  <span class="label-text-alt mt-1 text-base-content/60">{MODE_DESC[form.mode]}</span>
                </label>
                <label class="form-control">
                  <span class="label-text">Status</span>
                  <select class="select select-bordered" bind:value={form.status}>
                    <option value="active">Active</option>
                    <option value="disabled">Disabled</option>
                  </select>
                  <span class="label-text-alt mt-1 text-base-content/60">
                    Only <code>active</code> channels start a worker.
                  </span>
                </label>
              </div>

              <label class="form-control">
                <span class="label-text">Run on (Server / VPS)</span>
                <select class="select select-bordered" bind:value={form.nodeId}>
                  <option value={null}>{me?.role === 'admin' ? 'Control plane (no node)' : '— select a server —'}</option>
                  {#each nodes as n}
                    <option value={n.id}>{n.name}{n.host ? ` (${n.host})` : ''} — {n.status}</option>
                  {/each}
                </select>
                <span class="label-text-alt mt-1 text-base-content/60">
                  {#if nodes.length === 0}
                    Belum ada server. Tambah di <a href="/settings" class="link">Settings → My Servers</a>.
                  {:else}
                    Channel akan dijalankan oleh node agent di VPS yang dipilih.
                  {/if}
                </span>
              </label>

              <div class="rounded-lg border border-base-300 p-3">
                <div class="grid gap-3 md:grid-cols-2 md:items-start">
                  <div>
                    <label class="label cursor-pointer justify-start gap-3 p-0">
                      <input
                        class="toggle toggle-warning toggle-sm"
                        bind:checked={form.syncEnabled}
                        type="checkbox"
                      />
                      <span class="label-text font-medium">Viewer sync &amp; R2 upload</span>
                    </label>
                    <p class="mt-1 text-xs text-base-content/60">
                      Trims playlist to a fixed offset behind live edge so all viewers see the same segment.
                      When R2 is configured, segments are uploaded to Cloudflare R2 and served from the
                      public bucket URL instead of the VPS.
                    </p>
                  </div>
                  {#if form.syncEnabled}
                    <label class="form-control">
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
              </div>

              <div class="rounded-lg border border-base-300 p-3">
                <label class="label cursor-pointer justify-start gap-3 p-0">
                  <input
                    class="toggle toggle-primary toggle-sm"
                    bind:checked={form.playbackTokenRequired}
                    type="checkbox"
                  />
                  <span class="label-text font-medium">Require playback token</span>
                </label>
                <p class="mt-1 text-xs text-base-content/60">
                  When enabled, the HLS playlist URL must include a short-lived <code>exp</code>+<code>sig</code> token
                  (or a logged-in admin Bearer token). Disable to make the playlist publicly playable —
                  anyone with the URL can stream.
                </p>
              </div>

              <div class="rounded-lg border border-base-300 p-3">
                <label class="label cursor-pointer justify-start gap-3 p-0">
                  <input
                    class="toggle toggle-success toggle-sm"
                    bind:checked={form.allowedOriginsBypass}
                    type="checkbox"
                  />
                  <span class="label-text font-medium">Embeddable anywhere (bypass allowed origins)</span>
                </label>
                <p class="mt-1 text-xs text-base-content/60">
                  When enabled, this channel ignores the per-user / per-channel Allowed Origins list:
                  the share &amp; embed links can be played from any domain, with CORS open to all and
                  no <code>frame-ancestors</code> restriction. Leave off to keep the whitelist enforced.
                </p>
              </div>

              <button
                class="btn btn-ghost btn-sm justify-start"
                type="button"
                on:click={() => (showAdvanced = !showAdvanced)}
              >
                {showAdvanced ? '▾' : '▸'} Advanced options
              </button>

              {#if showAdvanced}
                <div class="grid gap-3 md:grid-cols-4 md:items-end">
                  <label class="form-control">
                    <span class="label-text text-xs" title="In-memory playlist cache TTL. Applies to all modes.">
                      Playlist TTL (s)
                    </span>
                    <input
                      class="input input-bordered input-sm"
                      bind:value={form.playlistTtlSeconds}
                      min="1"
                      type="number"
                    />
                  </label>
                  <label class="form-control">
                    <span
                      class="label-text text-xs"
                      title="Local segment cache freshness (ingest/proxy only — transmux uses retention instead)."
                    >
                      Segment TTL (s)
                    </span>
                    <input
                      class="input input-bordered input-sm"
                      bind:value={form.segmentTtlSeconds}
                      min="1"
                      type="number"
                      disabled={form.mode === 'transmux'}
                    />
                  </label>
                  <label class="form-control">
                    <span class="label-text text-xs" title="HLS playlist poll interval. Ingest mode only.">
                      Poll (s)
                    </span>
                    <input
                      class="input input-bordered input-sm"
                      bind:value={form.ingestPollSeconds}
                      min="1"
                      type="number"
                      disabled={form.mode !== 'ingest'}
                    />
                  </label>
                  <label class="label cursor-pointer justify-start gap-3 opacity-60" title="Stored in DB but not wired up in current backend.">
                    <input class="toggle toggle-primary toggle-sm" bind:checked={form.cacheEnabled} type="checkbox" disabled />
                    <span class="label-text text-xs">Cache <span class="text-warning">(N/A)</span></span>
                  </label>
                </div>
                <p class="-mt-1 text-xs text-base-content/60">
                  <b>Playlist TTL</b>: how long the rewritten <code>.m3u8</code> is cached in RAM.
                  <b>Segment TTL</b>: freshness of local segments before re-fetching upstream.
                  <b>Poll</b>: ingest worker fetches the upstream playlist this often.
                </p>
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
                  <button class="btn btn-sm" type="button" on:click={() => copy(shareResult?.playlistUrl ?? '')}>
                    <Icon icon="line-md:clipboard-list" />
                  </button>
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
                  <button class="btn btn-sm" type="button" on:click={() => copy(shareResult?.embedUrl ?? '')}>
                    <Icon icon="line-md:clipboard-list" />
                  </button>
                  <a class="btn btn-sm flex items-center justify-center" href={shareResult.embedUrl} target="_blank" rel="noopener noreferrer">
                    <Icon icon="line-md:external-link" />
                  </a>
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
