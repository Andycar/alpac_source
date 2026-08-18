# Admin v2 + Telegram WebApp

Premium admin panel for lampac-go, built with **Lit** (web components) + native
CSS. Same bundle serves the desktop admin under `/<adminPath>/v2/` and the
Telegram WebApp under `/tg-admin/`.

## Layout

```
web/admin/
  embed.go               # go:embed entry; pulls everything below into the binary
  index.html             # web admin shell (cookie auth)
  tg.html                # Telegram WebApp shell (initData auth)
  app.js                 # SPA bootstrap — routes + auth + shell wiring
  router.js              # hash router
  api.js                 # /api wrapper, auth-aware
  tg-bridge.js           # Telegram.WebApp adapter (no-op outside TG)
  styles/
    tokens.css           # design tokens (colors, fonts, motion)
    base.css             # reset + layout primitives
  components/
    _lit.js              # single Lit import point (pinned ESM URL)
    index.js             # barrel — auto-registers all custom elements
    l-shell.js           # sidebar + topbar layout
    l-card.js            # premium surface
    l-button.js          # button with variants
    l-stat.js            # big metric tile + sparkline
    l-pill.js            # status chip
    l-table.js           # sortable table
    l-chart.js           # canvas line/area chart
    l-toast.js           # global toast stack (imperative API)
    l-modal.js           # backdrop-blur dialog
    l-tabs.js            # segmented tabs
  pages/
    dashboard.js
    telemetry.js
    ...                  # one file per route
```

## Adding a page

1. Create `pages/<name>.js`:
   ```js
   import { html, render } from 'https://esm.sh/lit-html@3.2.1';
   import { api } from '../api.js';

   export async function render($mount, ctx) {
     const data = await api.get('/some-endpoint');
     render(html`
       <l-card title="Hello">${data.foo}</l-card>
     `, $mount);
   }
   ```
2. Register the route in `app.js`:
   ```js
   router.register({
     key: 'my-page',
     title: 'My Page',
     icon: '✨',
     group: 'Управление',
     loader: () => import('./pages/my-page.js'),
   });
   ```
3. Done — sidebar entry, route, and lazy-load are all wired.

## Telegram WebApp

The same bundle runs inside Telegram via `/tg-admin/`. The bridge in
`tg-bridge.js` auto-detects `window.Telegram.WebApp`, applies the bot's
theme params, and validates `initData` against the backend at
`POST /api/tg-admin/auth`. On success the server issues the standard
`lampac_token` cookie, so the rest of the admin API works identically.

**Onboarding:** an admin must do the normal `/tg/auth` flow once in a
browser before the WebApp can attach to their token. We deliberately
**do not** mint new tokens from a WebApp opening — that would let any
admin with TG access bypass the device-fingerprint bind.

## Coexistence with legacy admin

The legacy panel at `/<adminPath>/` keeps working unchanged. v2 is mounted
*alongside* it; admins can pick either. Each v2 page links back to the
legacy panel via a topbar shortcut so missing features remain reachable.

## Build / deploy

Zero build step. `embed.go` pulls every file into the Go binary via
`go:embed`. Lit and lit-html are loaded from `esm.sh` on first page hit
and cached by the browser. When we self-host the bundle in a future
iteration, swap the ESM URLs in `components/_lit.js` and `pages/*.js`
for local paths — no other code changes.

## Tokens / theming

The CSS custom properties in `styles/tokens.css` are the design contract.
Pages and components should reference them — never hardcode colors,
spacing, or motion curves. Three theming modes are supported:

- `:root` (default): dark, brand-led
- `:root.theme-auto` + `prefers-color-scheme: light`: courtesy light mode
- `:root.theme-tg`: the TG bridge overrides core variables from
  `Telegram.WebApp.themeParams` so the WebApp matches the user's Telegram
  appearance.
