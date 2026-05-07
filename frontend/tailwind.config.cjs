module.exports = {
  content: ['./src/**/*.{html,js,svelte,ts}'],
  theme: {
    extend: {}
  },
  plugins: [require('daisyui')],
  daisyui: {
    themes: [
      {
        restream: {
          primary: '#2563eb',
          secondary: '#0f766e',
          accent: '#d97706',
          neutral: '#1f2937',
          'base-100': '#f8fafc',
          info: '#0284c7',
          success: '#16a34a',
          warning: '#d97706',
          error: '#dc2626'
        }
      }
    ]
  }
};
