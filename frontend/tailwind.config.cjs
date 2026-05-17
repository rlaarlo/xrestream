module.exports = {
  content: ['./src/**/*.{html,js,svelte,ts}'],
  theme: {
    extend: {
      fontFamily: {
        sans: [
          'Inter',
          'ui-sans-serif',
          'system-ui',
          '-apple-system',
          'Segoe UI',
          'Roboto',
          'Helvetica',
          'Arial',
          'sans-serif'
        ]
      },
      boxShadow: {
        soft: '0 1px 2px 0 rgb(15 23 42 / 0.04), 0 1px 3px 0 rgb(15 23 42 / 0.06)'
      }
    }
  },
  plugins: [require('daisyui')],
  daisyui: {
    themes: [
      {
        restream: {
          primary: '#2563eb',
          'primary-content': '#ffffff',
          secondary: '#0f766e',
          'secondary-content': '#ffffff',
          accent: '#d97706',
          'accent-content': '#1c1917',
          neutral: '#1f2937',
          'neutral-content': '#f8fafc',
          'base-100': '#ffffff',
          'base-200': '#eef2f7',
          'base-300': '#cbd5e1',
          'base-content': '#0f172a',
          info: '#0284c7',
          'info-content': '#ffffff',
          success: '#16a34a',
          'success-content': '#ffffff',
          warning: '#d97706',
          'warning-content': '#1c1917',
          error: '#dc2626',
          'error-content': '#ffffff',
          '--rounded-box': '0.9rem',
          '--rounded-btn': '0.55rem',
          '--rounded-badge': '0.4rem'
        }
      },
      {
        'restream-dark': {
          primary: '#60a5fa',
          'primary-content': '#0b1220',
          secondary: '#2dd4bf',
          'secondary-content': '#0b1220',
          accent: '#fbbf24',
          'accent-content': '#1c1917',
          neutral: '#0f172a',
          'neutral-content': '#e2e8f0',
          'base-100': '#111a2e',
          'base-200': '#070b16',
          'base-300': '#334155',
          'base-content': '#e2e8f0',
          info: '#38bdf8',
          'info-content': '#0b1220',
          success: '#22c55e',
          'success-content': '#0b1220',
          warning: '#f59e0b',
          'warning-content': '#1c1917',
          error: '#f87171',
          'error-content': '#1c1917',
          '--rounded-box': '0.9rem',
          '--rounded-btn': '0.55rem',
          '--rounded-badge': '0.4rem'
        }
      }
    ],
    darkTheme: 'restream-dark',
    logs: false
  }
};
