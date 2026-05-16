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
  lastRequestAt?: string;
  lastSourceFetchAt?: string;
  lastSourceStatus?: number;
  lastError?: string;
  workerStartedAt?: string;
  createdAt?: string;
  updatedAt?: string;
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
