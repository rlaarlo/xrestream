import { loadApiBase } from '$lib/api';

export const ssr = false;
export const prerender = false;
export const trailingSlash = 'never';

export const load = async () => {
  await loadApiBase();
  return {};
};
