import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { resolve } from 'path';

export default defineConfig({
  plugins: [
    svelte({
      compilerOptions: {
        // Svelte 5 runes mode
        runes: true,
        // Inject CSS into JS (single file output)
        css: 'injected',
        // Disable a11y warnings — media player uses global keyboard shortcuts
        warningFilter: (warning) => !warning.code.startsWith('a11y'),
      },
    }),
  ],
  build: {
    lib: {
      entry: resolve(__dirname, 'src/main.ts'),
      formats: ['iife'],
      name: 'LampacWebPlayer',
      fileName: () => 'webplayer.js',
    },
    outDir: resolve(__dirname, '..', 'ubuntu', 'plugins'),
    emptyOutDir: false,
    minify: 'terser',
    terserOptions: {
      compress: {
        drop_console: false,
      },
    },
    rollupOptions: {
      external: ['shaka-player/dist/shaka-player.compiled'],
      output: {
        // No code splitting — single file
        inlineDynamicImports: true,
        globals: {
          'shaka-player/dist/shaka-player.compiled': 'shaka',
        },
      },
    },
  },
});
