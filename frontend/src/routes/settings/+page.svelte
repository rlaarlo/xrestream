<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from '@iconify/svelte';
  import ThemeToggle from '$lib/ThemeToggle.svelte';
  import {
    listOrigins, createOrigin, updateOrigin, deleteOrigin,
    listUsers, createUser, updateUser, deleteUser, setUserPassword,
    listNodes, createNode, updateNode, deleteNode, listAllNodes,
    getR2Config, saveR2Config, deleteR2Config,
    fetchMe, getStoredToken, setStoredToken, getApiBase, logout as apiLogout,
    type AllowedOrigin, type User, type AuthMe, type Node, type R2Config
  } from '$lib/api';
  import { goto } from '$app/navigation';

  let me: AuthMe | null = null;
  let tab: 'servers' | 'all-servers' | 'r2' | 'origins' | 'users' = 'servers';

  let origins: AllowedOrigin[] = [];
  let users: User[] = [];
  let loading = true;
  let error = '';
  let notice = '';

  let newOrigin = '';
  let newOriginLabel = '';
  let savingOrigin = false;

  let newUsername = '';
  let newPassword = '';
  let newRole: 'admin' | 'viewer' = 'admin';
  let savingUser = false;

  let pwModal: User | null = null;
  let pwValue = '';

  // Users filter / search / pagination
  let userSearch = '';
  let userRoleFilter: 'all' | 'admin' | 'viewer' = 'all';
  let userStatusFilter: 'all' | 'enabled' | 'disabled' = 'all';
  let userPage = 1;
  let userPageSize = 10;
  $: filteredUsers = users.filter((u) => {
    const q = userSearch.trim().toLowerCase();
    if (q && !u.username.toLowerCase().includes(q)) return false;
    if (userRoleFilter !== 'all' && u.role !== userRoleFilter) return false;
    if (userStatusFilter === 'enabled' && !u.enabled) return false;
    if (userStatusFilter === 'disabled' && u.enabled) return false;
    return true;
  });
  $: userTotalPages = Math.max(1, Math.ceil(filteredUsers.length / userPageSize));
  $: if (userPage > userTotalPages) userPage = userTotalPages;
  $: pagedUsers = filteredUsers.slice((userPage - 1) * userPageSize, userPage * userPageSize);
  $: {
    userSearch; userRoleFilter; userStatusFilter; userPageSize;
    userPage = 1;
  }

  // Servers
  let nodes: Node[] = [];
  let newNodeName = '';
  let newNodeHost = '';
  let savingNode = false;
  let createdKey: { node: Node; apiKey: string } | null = null;
  let installArch: 'auto' | 'amd64' | 'arm64' = 'auto';

  // Admin: all nodes across owners
  let allNodes: Node[] = [];
  let allNodesSearch = '';
  $: ownerMap = new Map(users.map((u) => [u.id, u.username] as const));
  $: filteredAllNodes = allNodes.filter((n) => {
    const q = allNodesSearch.trim().toLowerCase();
    if (!q) return true;
    const owner = (ownerMap.get(n.ownerId) || '').toLowerCase();
    return (
      n.name.toLowerCase().includes(q) ||
      (n.host || '').toLowerCase().includes(q) ||
      owner.includes(q) ||
      n.ownerId.toLowerCase().includes(q)
    );
  });

  // R2
  let r2: R2Config | null = null;
  let r2Form = { accountId: '', accessKeyId: '', secretAccessKey: '', bucket: '', publicUrl: '' };
  let savingR2 = false;

  $: if (notice) setTimeout(() => (notice = ''), 3500);
  $: if (error) setTimeout(() => (error = ''), 5000);

  onMount(async () => {
    if (!getStoredToken()) {
      goto('/');
      return;
    }
    try {
      me = await fetchMe();
    } catch {
      setStoredToken('');
      goto('/');
      return;
    }
    await refresh();
  });

  async function refresh() {
    loading = true;
    try {
      const isAdmin = me?.role === 'admin';
      const tasks: Promise<unknown>[] = [
        listNodes().then((v) => (nodes = v)),
        getR2Config().then((v) => {
          r2 = v;
          if (v) r2Form = { accountId: v.accountId, accessKeyId: v.accessKeyId, secretAccessKey: '', bucket: v.bucket, publicUrl: v.publicUrl };
        }),
        listOrigins().then((v) => (origins = v))
      ];
      if (isAdmin) {
        tasks.push(listUsers().then((v) => (users = v)));
        tasks.push(listAllNodes().then((v) => (allNodes = v)).catch(() => (allNodes = [])));
      }
      await Promise.all(tasks);
    } catch (e) {
      error = (e as Error).message;
    } finally {
      loading = false;
    }
  }

  // --- Servers ---
  async function addNode() {
    if (!newNodeName.trim()) return;
    savingNode = true;
    error = '';
    try {
      const res = await createNode(newNodeName.trim(), newNodeHost.trim());
      createdKey = res;
      newNodeName = '';
      newNodeHost = '';
      notice = 'Server created.';
      await refresh();
    } catch (e) {
      error = (e as Error).message;
    } finally {
      savingNode = false;
    }
  }
  async function removeNode(n: Node) {
    if (!confirm(`Delete server "${n.name}"?`)) return;
    try {
      await deleteNode(n.id);
      notice = 'Server deleted.';
      await refresh();
    } catch (e) {
      error = (e as Error).message;
    }
  }
  function installCommand(apiKey: string, arch: 'auto' | 'amd64' | 'arm64' = 'auto'): string {
    const base = getApiBase();
    const run = `MODE=node CONTROL_PLANE_URL=${base} NODE_API_KEY=${apiKey} ./restream-api`;
    if (arch === 'auto') {
      return [
        `ARCH=$(uname -m); case "$ARCH" in x86_64) A=amd64;; aarch64|arm64) A=arm64;; *) echo "unsupported arch $ARCH"; exit 1;; esac`,
        `curl -fSL -o restream-api "${base}/agent/restream-api-linux-$A"`,
        `chmod +x restream-api`,
        run
      ].join('\n');
    }
    return [
      `curl -fSL -o restream-api "${base}/agent/restream-api-linux-${arch}"`,
      `chmod +x restream-api`,
      run
    ].join('\n');
  }
  async function copyText(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      notice = 'Copied.';
    } catch {
      error = 'Copy failed.';
    }
  }

  // --- R2 ---
  async function saveR2() {
    if (!r2Form.accountId || !r2Form.accessKeyId || !r2Form.bucket) {
      error = 'Account ID, Access Key ID, and Bucket are required.';
      return;
    }
    savingR2 = true;
    error = '';
    try {
      const saved = await saveR2Config(r2Form);
      r2 = saved;
      r2Form.secretAccessKey = '';
      notice = 'R2 config saved.';
    } catch (e) {
      error = (e as Error).message;
    } finally {
      savingR2 = false;
    }
  }
  async function clearR2() {
    if (!confirm('Delete R2 config?')) return;
    try {
      await deleteR2Config();
      r2 = null;
      r2Form = { accountId: '', accessKeyId: '', secretAccessKey: '', bucket: '', publicUrl: '' };
      notice = 'R2 config deleted.';
    } catch (e) {
      error = (e as Error).message;
    }
  }

  async function addOrigin() {
    if (!newOrigin.trim()) return;
    savingOrigin = true;
    error = '';
    try {
      await createOrigin(newOrigin.trim(), newOriginLabel.trim());
      newOrigin = '';
      newOriginLabel = '';
      notice = 'Origin added.';
      await refresh();
    } catch (e) {
      error = (e as Error).message;
    } finally {
      savingOrigin = false;
    }
  }

  async function toggleOrigin(o: AllowedOrigin) {
    try {
      await updateOrigin(o.id, o.label, !o.enabled);
      await refresh();
    } catch (e) {
      error = (e as Error).message;
    }
  }

  async function removeOrigin(o: AllowedOrigin) {
    if (!confirm(`Delete origin "${o.origin}"?`)) return;
    try {
      await deleteOrigin(o.id);
      notice = 'Origin deleted.';
      await refresh();
    } catch (e) {
      error = (e as Error).message;
    }
  }

  async function addUser() {
    if (!newUsername.trim() || newPassword.length < 6) {
      error = 'Username required, password >= 6 chars.';
      return;
    }
    savingUser = true;
    error = '';
    try {
      await createUser(newUsername.trim(), newPassword, newRole);
      newUsername = '';
      newPassword = '';
      newRole = 'admin';
      notice = 'User created.';
      await refresh();
    } catch (e) {
      error = (e as Error).message;
    } finally {
      savingUser = false;
    }
  }

  async function toggleUser(u: User) {
    try {
      await updateUser(u.id, u.role, !u.enabled);
      await refresh();
    } catch (e) {
      error = (e as Error).message;
    }
  }

  async function changeRole(u: User, role: 'admin' | 'viewer') {
    try {
      await updateUser(u.id, role, u.enabled);
      await refresh();
    } catch (e) {
      error = (e as Error).message;
    }
  }

  async function removeUser(u: User) {
    if (!confirm(`Delete user "${u.username}"?`)) return;
    try {
      await deleteUser(u.id);
      notice = 'User deleted.';
      await refresh();
    } catch (e) {
      error = (e as Error).message;
    }
  }

  function openPwModal(u: User) {
    pwModal = u;
    pwValue = '';
  }

  async function savePassword() {
    if (!pwModal || pwValue.length < 6) {
      error = 'Password must be >= 6 characters.';
      return;
    }
    try {
      await setUserPassword(pwModal.id, pwValue);
      notice = `Password updated for ${pwModal.username}.`;
      pwModal = null;
      pwValue = '';
    } catch (e) {
      error = (e as Error).message;
    }
  }

  async function logout() {
    try {
      await apiLogout();
    } catch {
      /* ignore */
    }
    setStoredToken('');
    goto('/');
  }
</script>

<div class="min-h-screen bg-base-200">
  <header class="sticky top-0 z-30 border-b border-base-300 bg-base-100/85 backdrop-blur supports-[backdrop-filter]:bg-base-100/70">
    <div class="mx-auto flex max-w-5xl items-center gap-2 px-4 py-3">
      <a href="/" class="btn btn-ghost btn-sm gap-1" aria-label="Back to dashboard">
        <Icon icon="lucide:arrow-left" />
        <span class="hidden sm:inline">Back</span>
      </a>
      <span class="grid h-8 w-8 place-items-center rounded-lg bg-primary/10 text-primary">
        <Icon icon="lucide:settings" class="text-base" />
      </span>
      <span class="text-base font-semibold">Settings</span>
      <div class="ml-auto flex items-center gap-1.5">
        {#if me}
          <span class="hidden items-center gap-1.5 rounded-md border border-base-300 px-2 py-1 text-xs sm:inline-flex">
            <span class="grid h-5 w-5 place-items-center rounded-full bg-primary/15 text-[10px] font-bold text-primary">
              {(me.username || '?').charAt(0).toUpperCase()}
            </span>
            <span class="font-medium">{me.username}</span>
            <span class="badge badge-ghost badge-xs">{me.role}</span>
          </span>
        {/if}
        <ThemeToggle />
        <button class="btn btn-ghost btn-sm gap-1" type="button" on:click={logout}>
          <Icon icon="lucide:log-out" class="text-base" />
          <span class="hidden sm:inline">Logout</span>
        </button>
      </div>
    </div>
  </header>

  <div class="container mx-auto max-w-5xl p-4 md:p-6">
    {#if notice}
      <div class="alert alert-success mb-3"><span>{notice}</span></div>
    {/if}
    {#if error}
      <div class="alert alert-error mb-3"><span>{error}</span></div>
    {/if}

    <div role="tablist" class="tabs tabs-boxed mb-4 bg-base-100 border border-base-300 p-1">
      <button role="tab" class="tab gap-1.5" class:tab-active={tab === 'servers'} on:click={() => (tab = 'servers')}>
        <Icon icon="lucide:server" class="text-base" /> My Servers
      </button>
      <button role="tab" class="tab gap-1.5" class:tab-active={tab === 'r2'} on:click={() => (tab = 'r2')}>
        <Icon icon="lucide:cloud" class="text-base" /> R2 Bucket
      </button>
      <button role="tab" class="tab gap-1.5" class:tab-active={tab === 'origins'} on:click={() => (tab = 'origins')}>
        <Icon icon="lucide:shield-check" class="text-base" /> Allowed Origins
      </button>
      {#if me?.role === 'admin'}
        <button role="tab" class="tab gap-1.5" class:tab-active={tab === 'all-servers'} on:click={() => (tab = 'all-servers')}>
          <Icon icon="lucide:network" class="text-base" /> All Servers
        </button>
        <button role="tab" class="tab gap-1.5" class:tab-active={tab === 'users'} on:click={() => (tab = 'users')}>
          <Icon icon="lucide:users" class="text-base" /> Users
        </button>
      {/if}
    </div>

    {#if loading}
      <div class="flex justify-center p-8"><span class="loading loading-spinner" /></div>
    {:else if tab === 'servers'}
      <div class="card bg-base-100 shadow">
        <div class="card-body">
          <h2 class="card-title text-base">Add Server (VPS)</h2>
          <p class="text-sm opacity-70">
            Daftarkan VPS / compute baru. Setelah create, kamu akan dapat
            <code>NODE_API_KEY</code> yang dipasang di VPS supaya bisa heartbeat ke control plane.
          </p>
          <div class="flex flex-col md:flex-row gap-2 mt-2">
            <input class="input input-bordered flex-1" placeholder="Name (e.g. VPS Amsterdam)" bind:value={newNodeName} />
            <input class="input input-bordered flex-1" placeholder="Host (optional, e.g. vps1.example.com)" bind:value={newNodeHost} />
            <button class="btn btn-primary" on:click={addNode} disabled={savingNode}>Create</button>
          </div>
        </div>
      </div>

      <div class="card bg-base-100 shadow mt-4">
        <div class="card-body p-0">
          <table class="table">
            <thead>
              <tr><th>Name</th><th>Host</th><th>Status</th><th>Last seen</th><th></th></tr>
            </thead>
            <tbody>
              {#each nodes as n (n.id)}
                <tr>
                  <td class="font-medium">{n.name}</td>
                  <td class="font-mono text-xs opacity-70">{n.host || '—'}</td>
                  <td>
                    {#if n.status === 'online'}
                      <span class="badge badge-success badge-sm gap-1"><span class="w-1.5 h-1.5 rounded-full bg-white" /> online</span>
                    {:else if n.status === 'offline'}
                      <span class="badge badge-error badge-sm">offline</span>
                    {:else}
                      <span class="badge badge-warning badge-sm">pending</span>
                    {/if}
                  </td>
                  <td class="text-xs opacity-60">{n.lastSeenAt ? new Date(n.lastSeenAt).toLocaleString() : 'never'}</td>
                  <td class="text-right">
                    <button class="btn btn-ghost btn-xs text-error" on:click={() => removeNode(n)}>
                      <Icon icon="lucide:trash-2" />
                    </button>
                  </td>
                </tr>
              {:else}
                <tr><td colspan="5" class="text-center opacity-60 py-6">No servers yet.</td></tr>
              {/each}
            </tbody>
          </table>
        </div>
      </div>
    {:else if tab === 'all-servers'}
      <div class="card bg-base-100 shadow">
        <div class="card-body">
          <div class="flex flex-col md:flex-row md:items-center gap-2">
            <div>
              <h2 class="card-title text-base">All Servers (admin)</h2>
              <p class="text-sm opacity-70">Read-only view dari semua node lintas user. CRUD tetap di "My Servers" milik owner masing-masing.</p>
            </div>
            <div class="md:ml-auto flex gap-2">
              <input class="input input-bordered input-sm w-56" placeholder="Search name / host / owner" bind:value={allNodesSearch} />
              <button class="btn btn-ghost btn-sm" on:click={refresh} title="Refresh">
                <Icon icon="lucide:refresh-cw" />
              </button>
            </div>
          </div>
        </div>
      </div>

      <div class="card bg-base-100 shadow mt-4">
        <div class="card-body p-0">
          <table class="table">
            <thead>
              <tr><th>Owner</th><th>Name</th><th>Host</th><th>Status</th><th>Last seen</th><th>Created</th></tr>
            </thead>
            <tbody>
              {#each filteredAllNodes as n (n.id)}
                <tr>
                  <td class="text-sm">
                    {#if ownerMap.get(n.ownerId)}
                      <span class="font-medium">{ownerMap.get(n.ownerId)}</span>
                    {:else}
                      <span class="font-mono text-xs opacity-60">{n.ownerId}</span>
                    {/if}
                  </td>
                  <td class="font-medium">{n.name}</td>
                  <td class="font-mono text-xs opacity-70">{n.host || '—'}</td>
                  <td>
                    {#if n.status === 'online'}
                      <span class="badge badge-success badge-sm gap-1"><span class="w-1.5 h-1.5 rounded-full bg-white" /> online</span>
                    {:else if n.status === 'offline'}
                      <span class="badge badge-error badge-sm">offline</span>
                    {:else}
                      <span class="badge badge-warning badge-sm">pending</span>
                    {/if}
                  </td>
                  <td class="text-xs opacity-60">{n.lastSeenAt ? new Date(n.lastSeenAt).toLocaleString() : 'never'}</td>
                  <td class="text-xs opacity-60">{new Date(n.createdAt).toLocaleString()}</td>
                </tr>
              {:else}
                <tr><td colspan="6" class="text-center opacity-60 py-6">No servers registered.</td></tr>
              {/each}
            </tbody>
          </table>
        </div>
      </div>
    {:else if tab === 'r2'}
      <div class="card bg-base-100 shadow">
        <div class="card-body">
          <h2 class="card-title text-base">Cloudflare R2 Bucket</h2>
          <p class="text-sm opacity-70">
            Konfigurasi R2 untuk akun kamu. Secret di-mask di response — isi kembali bila ingin mengganti.
          </p>
          <div class="grid grid-cols-1 md:grid-cols-2 gap-2 mt-2">
            <input class="input input-bordered" placeholder="Account ID" bind:value={r2Form.accountId} />
            <input class="input input-bordered" placeholder="Bucket" bind:value={r2Form.bucket} />
            <input class="input input-bordered" placeholder="Access Key ID" bind:value={r2Form.accessKeyId} />
            <input class="input input-bordered" type="password" placeholder={r2 ? 'Secret Access Key (leave blank to keep)' : 'Secret Access Key'} bind:value={r2Form.secretAccessKey} />
            <input class="input input-bordered md:col-span-2" placeholder="Public URL (https://pub-xxx.r2.dev)" bind:value={r2Form.publicUrl} />
          </div>
          <div class="flex gap-2 mt-3">
            <button class="btn btn-primary" on:click={saveR2} disabled={savingR2}>{r2 ? 'Update' : 'Save'}</button>
            {#if r2}
              <button class="btn btn-ghost text-error" on:click={clearR2}>Delete</button>
              <span class="text-xs opacity-60 self-center ml-auto">Current secret: {r2.secretAccessKey || '—'}</span>
            {/if}
          </div>
        </div>
      </div>
    {:else if tab === 'origins'}
      <div class="card bg-base-100 shadow">
        <div class="card-body">
          <h2 class="card-title text-base">Add Origin</h2>
          <p class="text-sm opacity-70">
            Origins yang boleh memutar stream. Kosong = allow all (development only).
          </p>
          <div class="flex flex-col md:flex-row gap-2 mt-2">
            <input class="input input-bordered flex-1" placeholder="https://your-site.com" bind:value={newOrigin} />
            <input class="input input-bordered md:w-48" placeholder="Label (optional)" bind:value={newOriginLabel} />
            <button class="btn btn-primary" on:click={addOrigin} disabled={savingOrigin}>Add</button>
          </div>
        </div>
      </div>

      <div class="card bg-base-100 shadow mt-4">
        <div class="card-body p-0">
          <table class="table">
            <thead>
              <tr><th>Origin</th><th>Label</th><th>Enabled</th><th>Created</th><th></th></tr>
            </thead>
            <tbody>
              {#each origins as o (o.id)}
                <tr>
                  <td class="font-mono text-sm">{o.origin}</td>
                  <td class="text-sm opacity-70">{o.label || '—'}</td>
                  <td>
                    <input type="checkbox" class="toggle toggle-sm toggle-success" checked={o.enabled} on:change={() => toggleOrigin(o)} />
                  </td>
                  <td class="text-xs opacity-60">{new Date(o.createdAt).toLocaleString()}</td>
                  <td class="text-right">
                    <button class="btn btn-ghost btn-xs text-error" on:click={() => removeOrigin(o)}>
                      <Icon icon="lucide:trash-2" />
                    </button>
                  </td>
                </tr>
              {:else}
                <tr><td colspan="5" class="text-center opacity-60 py-6">No origins. Stream is open to all.</td></tr>
              {/each}
            </tbody>
          </table>
        </div>
      </div>
    {:else}
      <div class="card bg-base-100 shadow">
        <div class="card-body">
          <h2 class="card-title text-base">Add User</h2>
          <div class="grid grid-cols-1 md:grid-cols-4 gap-2 mt-2">
            <input class="input input-bordered" placeholder="username" bind:value={newUsername} />
            <input class="input input-bordered" type="password" placeholder="password (min 6)" bind:value={newPassword} />
            <select class="select select-bordered" bind:value={newRole}>
              <option value="admin">admin</option>
              <option value="viewer">viewer</option>
            </select>
            <button class="btn btn-primary" on:click={addUser} disabled={savingUser}>Create</button>
          </div>
        </div>
      </div>

      <div class="card bg-base-100 shadow mt-4">
        <div class="card-body">
          <div class="flex flex-col md:flex-row md:items-center gap-2">
            <label class="input input-bordered input-sm flex items-center gap-2 flex-1">
              <Icon icon="lucide:search" class="opacity-60" />
              <input type="text" class="grow" placeholder="Search username" bind:value={userSearch} />
            </label>
            <select class="select select-bordered select-sm" bind:value={userRoleFilter}>
              <option value="all">All roles</option>
              <option value="admin">admin</option>
              <option value="viewer">viewer</option>
            </select>
            <select class="select select-bordered select-sm" bind:value={userStatusFilter}>
              <option value="all">All status</option>
              <option value="enabled">Enabled</option>
              <option value="disabled">Disabled</option>
            </select>
            <select class="select select-bordered select-sm" bind:value={userPageSize}>
              <option value={10}>10 / page</option>
              <option value={25}>25 / page</option>
              <option value={50}>50 / page</option>
              <option value={100}>100 / page</option>
            </select>
          </div>
          <div class="text-xs opacity-60 mt-2">Showing {pagedUsers.length} of {filteredUsers.length} (total {users.length})</div>
        </div>
        <div class="card-body p-0">
          <table class="table">
            <thead>
              <tr><th>Username</th><th>Role</th><th>Enabled</th><th>Last login</th><th></th></tr>
            </thead>
            <tbody>
              {#each pagedUsers as u (u.id)}
                <tr>
                  <td class="font-medium">{u.username}{#if me && u.username === me.username}<span class="badge badge-ghost badge-xs ml-2">you</span>{/if}</td>
                  <td>
                    <select class="select select-bordered select-xs" value={u.role} on:change={(e) => changeRole(u, (e.currentTarget as HTMLSelectElement).value as 'admin' | 'viewer')}>
                      <option value="admin">admin</option>
                      <option value="viewer">viewer</option>
                    </select>
                  </td>
                  <td><input type="checkbox" class="toggle toggle-sm toggle-success" checked={u.enabled} on:change={() => toggleUser(u)} /></td>
                  <td class="text-xs opacity-60">{u.lastLoginAt ? new Date(u.lastLoginAt).toLocaleString() : 'never'}</td>
                  <td class="text-right whitespace-nowrap">
                    <button class="btn btn-ghost btn-xs" on:click={() => openPwModal(u)} title="Change password">
                      <Icon icon="lucide:key-round" />
                    </button>
                    <button class="btn btn-ghost btn-xs text-error" on:click={() => removeUser(u)} title="Delete">
                      <Icon icon="lucide:trash-2" />
                    </button>
                  </td>
                </tr>
              {:else}
                <tr><td colspan="5" class="text-center opacity-60 py-6">No users match the filters.</td></tr>
              {/each}
            </tbody>
          </table>
        </div>
        <div class="card-body pt-2 flex flex-row items-center justify-between">
          <div class="text-xs opacity-60">Page {userPage} of {userTotalPages}</div>
          <div class="join">
            <button class="join-item btn btn-sm" on:click={() => (userPage = 1)} disabled={userPage <= 1}>«</button>
            <button class="join-item btn btn-sm" on:click={() => (userPage = Math.max(1, userPage - 1))} disabled={userPage <= 1}>‹</button>
            <button class="join-item btn btn-sm btn-ghost no-animation">{userPage} / {userTotalPages}</button>
            <button class="join-item btn btn-sm" on:click={() => (userPage = Math.min(userTotalPages, userPage + 1))} disabled={userPage >= userTotalPages}>›</button>
            <button class="join-item btn btn-sm" on:click={() => (userPage = userTotalPages)} disabled={userPage >= userTotalPages}>»</button>
          </div>
        </div>
      </div>
    {/if}
  </div>

  {#if pwModal}
    <div class="modal modal-open">
      <div class="modal-box">
        <h3 class="text-lg font-semibold mb-2">Change password — {pwModal.username}</h3>
        <input class="input input-bordered w-full" type="password" placeholder="new password" bind:value={pwValue} />
        <p class="text-xs opacity-60 mt-2">User's existing sessions will be invalidated.</p>
        <div class="modal-action">
          <button class="btn btn-ghost" on:click={() => (pwModal = null)}>Cancel</button>
          <button class="btn btn-primary" on:click={savePassword}>Save</button>
        </div>
      </div>
    </div>
  {/if}

  {#if createdKey}
    <div class="modal modal-open">
      <div class="modal-box max-w-2xl">
        <h3 class="text-lg font-semibold mb-2">Server "{createdKey.node.name}" created</h3>
        <p class="text-sm opacity-70 mb-3">
          API key di bawah hanya ditampilkan <strong>sekali</strong>. Simpan baik-baik dan pasang di VPS kamu.
        </p>
        <div class="form-control">
          <label class="label py-1" for="apikey"><span class="label-text text-xs">NODE_API_KEY</span></label>
          <div class="join">
            <input id="apikey" class="input input-bordered join-item w-full font-mono text-xs" readonly value={createdKey.apiKey} />
            <button class="btn btn-square join-item" on:click={() => createdKey && copyText(createdKey.apiKey)}>
              <Icon icon="lucide:copy" />
            </button>
          </div>
        </div>
        <div class="form-control mt-3">
          <label class="label py-1" for="installcmd"><span class="label-text text-xs">Run on your VPS — pilih sesuai arsitektur</span></label>
          <div role="tablist" class="tabs tabs-boxed mb-2 w-fit">
            <button role="tab" class="tab tab-sm" class:tab-active={installArch === 'auto'} on:click={() => (installArch = 'auto')}>Auto-detect</button>
            <button role="tab" class="tab tab-sm" class:tab-active={installArch === 'amd64'} on:click={() => (installArch = 'amd64')}>amd64 (x86_64)</button>
            <button role="tab" class="tab tab-sm" class:tab-active={installArch === 'arm64'} on:click={() => (installArch = 'arm64')}>arm64 (aarch64)</button>
          </div>
          <div class="join">
            <textarea id="installcmd" class="textarea textarea-bordered join-item w-full font-mono text-xs" rows={installArch === 'auto' ? 4 : 3} readonly value={installCommand(createdKey.apiKey, installArch)}></textarea>
            <button class="btn btn-square join-item" on:click={() => createdKey && copyText(installCommand(createdKey.apiKey, installArch))}>
              <Icon icon="lucide:copy" />
            </button>
          </div>
        </div>
        <div class="modal-action">
          <button class="btn btn-primary" on:click={() => (createdKey = null)}>Done</button>
        </div>
      </div>
    </div>
  {/if}
</div>
