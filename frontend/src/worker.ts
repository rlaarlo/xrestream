export interface Env {
  API_BASE_URL?: string;
  ASSETS: Fetcher;
}

export default {
  async fetch(req: Request, env: Env): Promise<Response> {
    const url = new URL(req.url);
    if (url.pathname === '/config.json') {
      return Response.json(
        { apiBaseUrl: env.API_BASE_URL ?? '' },
        { headers: { 'cache-control': 'no-store' } }
      );
    }
    return env.ASSETS.fetch(req);
  }
};
