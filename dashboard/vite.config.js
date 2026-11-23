import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));

// In production nginx serves the guide page at "/". This plugin does the same for
// `npm run dev`, so "/" is the guide and "/dashboard/" is the app in both setups.
const guideFiles = {
  '/': 'index.html',
  '/index.html': 'index.html',
  '/request-flow.png': 'request-flow.png',
  '/architecture.png': 'architecture.png',
};
const guide = () => ({
  name: 'llmgate-guide',
  configureServer(server) {
    server.middlewares.use((req, res, next) => {
      const file = guideFiles[(req.url || '').split('?')[0]];
      if (!file) return next();
      res.setHeader('Content-Type', file.endsWith('.png') ? 'image/png' : 'text/html; charset=utf-8');
      fs.createReadStream(path.resolve(here, '../docs', file)).pipe(res);
    });
  },
});

export default defineConfig({
  // The guide page is served at "/"; the React app lives under /dashboard/.
  base: '/dashboard/',
  plugins: [react(), tailwindcss(), guide()],
  resolve: {
    // Seed JSON lives in ../data so the Go gateway and this UI share one pricing file.
    alias: { '@data': path.resolve(here, '../data') },
  },
  server: {
    port: 5173,
    // In dev, forward /api to a locally running gateway (make run-gateway).
    proxy: { '/api': 'http://localhost:8080' },
    fs: { allow: ['..'] },
  },
});
