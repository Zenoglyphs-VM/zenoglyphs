import {defineConfig} from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import {resolve} from 'path'

// The build output is what `web/embed.go` embeds, so `zcribe serve` still ships
// one binary with no files on disk beside it.
//
// Two properties of the old hand-written viewer are load-bearing and are kept
// here deliberately:
//
//   - Nothing is fetched from a CDN at runtime. Every dependency, the webfonts
//     included (nom-ui pulls them in via @fontsource), is bundled into the
//     output, so the viewer renders identically on a machine with no internet
//     running against a local devnet.
//   - Asset URLs are relative (`base: './'`), so the viewer works unchanged
//     whether it is served from the root or mounted under a path.
export default defineConfig({
  base: './',
  plugins: [vue(), tailwindcss()],
  resolve: {
    alias: {
      '@': resolve(__dirname, './src'),
    },
  },
  build: {
    outDir: resolve(__dirname, '../dist'),
    emptyOutDir: true,
    // Not the Vite default "assets": the API already answers GET /assets with
    // the inscription list, and two different things under one path name is a
    // trap even though Go's router happens to tell them apart.
    assetsDir: 'static',
    // Fonts and other assets stay separate files rather than being inlined as
    // data URIs: the binary is the same size either way, and separate files
    // can be cached by the browser across reloads.
    assetsInlineLimit: 0,
    rollupOptions: {
      // Two pages, not one app with a second route: the admin panel is never
      // linked from the viewer and must not ship inside its bundle. Because
      // this input lives at admin/index.html (a subdirectory, not
      // admin.html), Vite mirrors that layout on the way out — dist/admin/
      // index.html — which is what makes plain GET /admin resolve to it
      // (net/http redirects the directory request, then serves its index).
      input: {
        main: resolve(__dirname, 'index.html'),
        admin: resolve(__dirname, 'admin/index.html'),
      },
    },
  },
  optimizeDeps: {
    // nom-ui ships Vue/TS source rather than a build, so it is compiled by this
    // app's pipeline instead of being pre-bundled.
    exclude: ['nom-ui'],
  },
  server: {
    // `npm run dev` talks to a running `zcribe serve` for the API; the
    // inscription bytes come from the separate content origin the API names in
    // /info, which is already an absolute URL and needs no proxying.
    proxy: Object.fromEntries(
      [
        '/info',
        '/assets',
        '/asset',
        '/address',
        '/collections',
        '/collection',
        '/dispensers',
        '/dispenser',
        '/momentum',
        '/stateroot',
        '/state',
        '/admin/api',
      ].map((p) => [p, {target: 'http://127.0.0.1:8710', changeOrigin: false}]),
    ),
  },
})
