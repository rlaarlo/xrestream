export type Channel = {
  id: string;
  name: string;
  slug: string;
  inputUrl: string;
  mode: 'ingest' | 'proxy' | 'transmux';
  status: 'active' | 'disabled' | 'source_error';
  workerStatus: 'running' | 'stopped' | 'error';
  playlistUrl?: string;
  playlistTtlSeconds: number;
  segmentTtlSeconds: number;
  ingestPollSeconds: number;
  cacheEnabled: boolean;
  syncEnabled: boolean;
  syncDelaySeconds: number;
  playbackTokenRequired: boolean;
  httpReferer: string;
  httpUserAgent: string;
  httpOrigin: string;
  lastRequestAt?: string;
  lastSourceFetchAt?: string;
  lastSourceStatus?: number;
  lastError?: string;
  workerStartedAt?: string;
  createdAt?: string;
  updatedAt?: string;
  ownerId?: string | null;
  nodeId?: string | null;
};

export type ChannelInput = {
  name: string;
  slug: string;
  inputUrl: string;
  mode: 'ingest' | 'proxy' | 'transmux';
  status: 'active' | 'disabled' | 'source_error';
  playlistTtlSeconds: number;
  segmentTtlSeconds: number;
  ingestPollSeconds: number;
  cacheEnabled: boolean;
  syncEnabled: boolean;
  syncDelaySeconds: number;
  playbackTokenRequired: boolean;
  httpReferer: string;
  httpUserAgent: string;
  httpOrigin: string;
  nodeId?: string | null;
};

export type Metrics = {
  playlistRequests: number;
  segmentRequests: number;
  upstreamRequests: number;
  cacheHits: number;
  cacheMisses: number;
  bytesSent: number;
  bytesUpstream: number;
  workerErrors: number;
  liveViewers?: number;
};

export type ShareLink = {
  playlistUrl: string;
  embedUrl: string;
  expiresAt: string;
};

export async function createShareLink(channelId: string, ttlMinutes: number) {
  return apiFetch<ShareLink>(`/channels/${channelId}/share-link`, {
    method: 'POST',
    body: JSON.stringify({ ttlMinutes })
  });
}

export type PlaybackToken = {
  playbackUrl: string;
  expiresAt: string;
  ttlSeconds: number;
};

export async function mintPlaybackToken(channelId: string) {
  return apiFetch<PlaybackToken>(`/channels/${channelId}/playback-token`, {
    method: 'POST'
  });
}

export type AllowedOrigin = {
  id: string;
  ownerId?: string | null;
  origin: string;
  label: string;
  enabled: boolean;
  createdAt: string;
};

export const listOrigins = () => apiFetch<AllowedOrigin[]>('/me/origins');
export const createOrigin = (origin: string, label = '') =>
  apiFetch<AllowedOrigin>('/me/origins', { method: 'POST', body: JSON.stringify({ origin, label }) });
export const updateOrigin = (id: string, label: string, enabled: boolean) =>
  apiFetch<AllowedOrigin>(`/me/origins/${id}`, { method: 'PATCH', body: JSON.stringify({ label, enabled }) });
export const deleteOrigin = (id: string) =>
  apiFetch<{ deleted: boolean }>(`/me/origins/${id}`, { method: 'DELETE' });

export type User = {
  id: string;
  username: string;
  role: 'admin' | 'viewer';
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
  lastLoginAt?: string;
};

export const listUsers = () => apiFetch<User[]>('/admin/users');
export const createUser = (username: string, password: string, role: 'admin' | 'viewer' = 'admin') =>
  apiFetch<User>('/admin/users', { method: 'POST', body: JSON.stringify({ username, password, role, enabled: true }) });
export const updateUser = (id: string, role: 'admin' | 'viewer', enabled: boolean) =>
  apiFetch<User>(`/admin/users/${id}`, { method: 'PATCH', body: JSON.stringify({ role, enabled }) });
export const setUserPassword = (id: string, password: string) =>
  apiFetch<{ ok: boolean }>(`/admin/users/${id}/password`, { method: 'POST', body: JSON.stringify({ password }) });
export const deleteUser = (id: string) =>
  apiFetch<{ deleted: boolean }>(`/admin/users/${id}`, { method: 'DELETE' });

export type AuthMe = { username: string; role: string; expiresAt: string };
export const fetchMe = () => apiFetch<AuthMe>('/auth/me');
export const logout = () => apiFetch<{ ok: boolean }>('/auth/logout', { method: 'POST' });

export type Node = {
  id: string;
  ownerId: string;
  name: string;
  host: string;
  status: 'pending' | 'online' | 'offline';
  lastSeenAt?: string;
  createdAt: string;
  updatedAt: string;
};

export const listNodes = (all = false) =>
  apiFetch<Node[]>(`/me/nodes${all ? '?all=1' : ''}`);
export const listAllNodes = () => apiFetch<Node[]>('/admin/nodes');
export const createNode = (name: string, host = '') =>
  apiFetch<{ node: Node; apiKey: string }>('/me/nodes', {
    method: 'POST',
    body: JSON.stringify({ name, host })
  });
export const updateNode = (id: string, name: string, host: string) =>
  apiFetch<Node>(`/me/nodes/${id}`, { method: 'PATCH', body: JSON.stringify({ name, host }) });
export const deleteNode = (id: string) =>
  apiFetch<{ deleted: boolean }>(`/me/nodes/${id}`, { method: 'DELETE' });

export type R2Config = {
  id: string;
  ownerId: string;
  accountId: string;
  accessKeyId: string;
  secretAccessKey?: string;
  bucket: string;
  publicUrl: string;
  createdAt: string;
  updatedAt: string;
};

export const getR2Config = () => apiFetch<R2Config | null>('/me/r2');
export const saveR2Config = (body: {
  accountId: string;
  accessKeyId: string;
  secretAccessKey: string;
  bucket: string;
  publicUrl: string;
}) => apiFetch<R2Config>('/me/r2', { method: 'PUT', body: JSON.stringify(body) });
export const deleteR2Config = () =>
  apiFetch<{ deleted: boolean }>('/me/r2', { method: 'DELETE' });

const fallbackApiBase = 'http://localhost:3000';

export function getApiBase() {
  return import.meta.env.VITE_API_BASE_URL || fallbackApiBase;
}

export function getStoredToken() {
  if (typeof localStorage === 'undefined') return '';
  return localStorage.getItem('adminToken') || '';
}

export function setStoredToken(token: string) {
  if (typeof localStorage === 'undefined') return;
  if (token) localStorage.setItem('adminToken', token);
  else localStorage.removeItem('adminToken');
}

export async function apiFetch<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers);
  headers.set('Content-Type', 'application/json');
  const token = getStoredToken();
  if (token) headers.set('Authorization', `Bearer ${token}`);

  const response = await fetch(`${getApiBase()}${path}`, {
    ...options,
    headers
  });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(payload.error || `Request failed with status ${response.status}`);
  }
  return payload as T;
}
