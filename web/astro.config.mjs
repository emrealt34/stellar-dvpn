import { defineConfig } from 'astro/config';
import react from '@astrojs/react';

// The site is served from a GitHub Pages project URL (/stellar-dvpn/), so the
// base path is fixed. The live network island calls the coordinator over an
// absolute URL because it is a different origin.
export default defineConfig({
  site: 'https://emrealt34.github.io',
  base: '/stellar-dvpn',
  output: 'static',
  integrations: [react()],
  vite: {
    define: {
      __API_BASE__: JSON.stringify('https://dmesh-coordinator.onrender.com'),
    },
  },
});
