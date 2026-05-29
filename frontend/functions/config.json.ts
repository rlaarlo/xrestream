interface Env {
  API_BASE_URL?: string;
}

export const onRequestGet: PagesFunction<Env> = ({ env }) => {
  return Response.json(
    { apiBaseUrl: env.API_BASE_URL ?? '' },
    { headers: { 'cache-control': 'no-store' } }
  );
};
