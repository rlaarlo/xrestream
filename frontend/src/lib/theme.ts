import { writable } from 'svelte/store';

export type ThemeMode = 'light' | 'dark';

const STORAGE_KEY = 'restream:theme';
const LIGHT = 'restream';
const DARK = 'restream-dark';

function resolveInitial(): ThemeMode {
  if (typeof document === 'undefined') return 'light';
  const stored = (typeof localStorage !== 'undefined' && localStorage.getItem(STORAGE_KEY)) as ThemeMode | null;
  if (stored === 'light' || stored === 'dark') return stored;
  if (typeof window !== 'undefined' && window.matchMedia?.('(prefers-color-scheme: dark)').matches) {
    return 'dark';
  }
  return 'light';
}

export const themeMode = writable<ThemeMode>(resolveInitial());

export function applyTheme(mode: ThemeMode) {
  if (typeof document === 'undefined') return;
  document.documentElement.setAttribute('data-theme', mode === 'dark' ? DARK : LIGHT);
  document.documentElement.style.colorScheme = mode;
}

export function setTheme(mode: ThemeMode) {
  themeMode.set(mode);
  if (typeof localStorage !== 'undefined') localStorage.setItem(STORAGE_KEY, mode);
  applyTheme(mode);
}

export function toggleTheme() {
  let current: ThemeMode = 'light';
  themeMode.update((v) => {
    current = v === 'dark' ? 'light' : 'dark';
    return current;
  });
  if (typeof localStorage !== 'undefined') localStorage.setItem(STORAGE_KEY, current);
  applyTheme(current);
}

// Sync to OS preference changes when user hasn't explicitly chosen.
if (typeof window !== 'undefined' && window.matchMedia) {
  const mq = window.matchMedia('(prefers-color-scheme: dark)');
  mq.addEventListener?.('change', (e) => {
    if (typeof localStorage !== 'undefined' && localStorage.getItem(STORAGE_KEY)) return;
    const next: ThemeMode = e.matches ? 'dark' : 'light';
    themeMode.set(next);
    applyTheme(next);
  });
}
