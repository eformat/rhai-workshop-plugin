# rhai-workshop-plugin

## Dependencies

- **rhai-nav-plugin** must be deployed first — it defines the "RHAI" top-level navigation section that this plugin's nav item references.

## Layout

A Console Plugin that contains 2 iframes with a resizable splitter.

1. Left Half of the screen. RedHat OpenShift AI. The url is dynamically retrieved from:

```bash
oc get ConsoleLink rhodslink -o jsonpath={.spec.href}
```

The left pane has a collapsible URL bar (PatternFly `SearchInput`) with:
- **Back/forward navigation** through URL history
- **Search/Go button** to navigate to a new URL (auto-prepends `https://` if missing)
- **Refresh button** to reload the current page
2. Right Half of the screen. Configurable tutorial links e.g. `https://eformat.github.io/voice-agents/voice-agents/index.html`

We also open the OpenShift command line terminal - this is always at the bottom of the page - full width.

## Architecture

The plugin uses a **Go HTTP backend** (gorilla/mux) that serves both the React frontend assets and authenticated API endpoints. All API requests go through the OpenShift console's **UserToken proxy**, which injects the user's Bearer token. The Go backend validates tokens via **TokenReview** and checks admin permissions via **SubjectAccessReview**.

### Authentication Flow

1. User authenticates to OpenShift via OAuth
2. Console proxy forwards the user's real token on `/api/proxy/plugin/rhai-workshop-plugin/backend/*` requests
3. Go backend validates via TokenReview → gets real username and groups
4. SubjectAccessReview checks admin permission (`rhaiworkshop.openshift.io/workshops` verb `admin`)
5. Auth results cached for 60 seconds (keyed by SHA256 token hash)

### API Endpoints

All `/api/*` routes require authentication (except `/api/health`):

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/health` | GET | Health check (no auth) |
| `/api/auth/me` | GET | Returns authenticated user info |
| `/api/config` | GET | Workshop config (openshiftAiUrl, tutorialUrls, showTabs) |
| `/api/proxy-users` | GET | List of GUIDs with active showroom proxy configs |
| `/api/tutorial-proxy/{guid}/{path}` | GET | Reverse proxy with per-user string substitution |

### ConsolePlugin CR

The `ConsolePlugin` resource includes a `proxy` section with `authorization: UserToken`, which tells the console to inject the user's Bearer token on all requests routed through `/api/proxy/plugin/rhai-workshop-plugin/backend/`.

## Iframe Embedding (IngressController Patch)

The OpenShift console sets `X-Frame-Options: DENY` and `Content-Security-Policy: frame-ancestors 'none'; frame-src 'none'` which prevents iframe embedding. The init container automatically patches the default IngressController to **delete** both the `X-Frame-Options` and `Content-Security-Policy` response headers. This affects all routes through the default ingress controller — acceptable for a workshop cluster.

## Configuration

The Go backend reads the `workshop-config` ConfigMap via the Kubernetes API at startup and discovers `openshiftAiUrl` from the `rhodslink` ConsoleLink. Falls back to hardcoded defaults if not found.

- **`openshiftAiUrl`** — auto-discovered from the cluster via ConsoleLink `rhodslink`. No manual config needed.
- **`tutorialUrls`** — set manually in the `workshop-config` ConfigMap in `gitops/base/rhai-workshop-deploy.yaml`.

`tutorialUrls` is a JSON array of `{"name": "...", "url": "..."}` objects. When multiple tutorials are configured, PatternFly tabs appear at the top of the right pane. With a single entry, no tabs are shown.

To change the tutorial URLs: edit the ConfigMap, then restart the pod (`oc rollout restart deployment/rhai-workshop-plugin -n rhai-workshop-plugin`).

## Behaviour

The plugin intercepts copy button clicks from the tutorial iframe and auto-pastes into the OpenShift web terminal.

### How it works

1. Tutorial code blocks with the `.copypaste` role get a copy button (provided by the Antora theme's clipboard.js)
2. A script in the tutorial's `footer-scripts.hbs` (defined inline in `site.yml` `supplemental_files`) sends `window.parent.postMessage({ type: 'copy', text: '...' }, '*')` when a copy button is clicked
3. The plugin listens for `postMessage` events and dispatches a `ClipboardEvent('paste')` on the xterm.js hidden textarea (`textarea.xterm-helper-textarea`), which is in the same DOM (not in an iframe)

### Tutorial side

- The postMessage script is defined in one place: `site.yml` under `ui.supplemental_files` as an inline `footer-scripts.hbs` override [see the code example here.](https://github.com/eformat/voice-agents/blob/main/site.yml#L15-L35)
- To add a copy-to-terminal button on any code block, use the `.copypaste` role in AsciiDoc [see the code example here.](https://github.com/eformat/voice-agents/blob/main/content/modules/ROOT/pages/index.adoc?plain=1#L57)

```asciidoc
[.copypaste,source,bash]
----
oc apply -f some-resource.yaml
----
```

- For Markdown copy-n-paste - just use the event - [see the code example here.](https://github.com/eformat/rainforest-docs/blob/main/index.html#L144-L157)

```html
  <script src="//cdn.jsdelivr.net/npm/docsify-copy-code"></script>
  <script>
    // Send copied code to parent frame (rhai-workshop-plugin) for auto-paste into terminal
    document.addEventListener('click', function (e) {
      var btn = e.target.closest('.docsify-copy-code-button');
      if (!btn) return;
      var pre = btn.closest('pre');
      if (!pre) return;
      var code = pre.querySelector('code');
      var text = (code || pre).textContent.trim();
      if (text && window.parent !== window) {
        window.parent.postMessage({ type: 'copy', text: text }, '*');
      }
    });
  </script>
```

## Showroom Proxy (Per-User Variable Substitution)

When deployed with the showroom overlay (`gitops/overlays/showroom`), the Go backend runs a built-in namespace watcher that provides per-user variable substitution in tutorial content.

### Architecture

1. **Showroom watcher** (built into the Go backend) — watches for `user-*-showroom` namespaces, reads `showroom-userdata` ConfigMaps, and stores per-user substitution rules in memory.
2. **Go reverse proxy** — each user gets a `/api/tutorial-proxy/<guid>/` path that proxies to GitHub Pages and applies string replacements in the response body (replacing placeholder defaults with real values).
3. **Auth enforcement** — the Go backend validates that the authenticated user's GUID matches the proxy path GUID (users can only access their own substituted content). Admins can access any path.
4. **React client** — `useCurrentUserGuid()` fetches `/api/proxy-users` and `/api/auth/me` (both authenticated). When the user's GUID is in the proxy list, `rewriteTutorialUrls()` rewrites tutorial iframe URLs through the proxy. Falls back to direct GitHub Pages URLs when proxy is not configured.

### How it works

- Watcher reconciles on namespace watch events (debounced) and on a periodic timer (default 30s).
- Proxy host is auto-detected from the first `https://` URL in `tutorialUrls` in the `workshop-config` ConfigMap.
- The watcher deduplicates substitution rules by default value (alphabetically first key wins) and sorts rules longest-first to avoid partial matches.
- Directory-style proxy URLs get a trailing `/` appended to avoid 301 redirects that break relative URL resolution through the console proxy chain.
- The clipboard polyfill script is injected before `</head>` in proxied HTML responses.

### Tutorial repo requirements

Each tutorial's `antora.yml` attributes must use the same default placeholder values as `showroomDefaults` in the `workshop-config` ConfigMap. Use `%placeholder%` patterns for values that would otherwise conflict (e.g. `password`):

```yaml
# content/antora.yml
asciidoc:
  attributes:
    guid: abc123
    bastion_public_hostname: bastion.example.com
    bastion_ssh_password: "%bastion_ssh_password%"
    password: "%password%"
    user: user-abc123
    openshift_console_url: https://console-openshift-console.apps.cluster.example.com
    openshift_cluster_ingress_domain: apps.cluster.example.com
    openshift_api_url: "https://api.cluster.example.com:6443"
```

## Build & Deploy

```bash
# Build and push plugin image (3-stage: frontend + Go backend + runtime)
make podman-push

# Deploy base (no showroom proxy)
oc apply -k ./gitops/base

# Deploy with showroom proxy overlay
oc apply -k ./gitops/overlays/showroom

# Restart after image update
oc rollout restart deployment/rhai-workshop-plugin -n rhai-workshop-plugin
```

## Project Structure

- `cmd/backend/main.go` - Go HTTP server entry point (router, config discovery, startup)
- `pkg/api/auth.go` - TokenReview + SubjectAccessReview auth middleware
- `pkg/api/config.go` - Workshop config endpoint
- `pkg/api/helpers.go` - JSON response utilities
- `pkg/proxy/tutorial.go` - Reverse proxy with string substitution (replaces nginx sub_filter)
- `pkg/proxy/watcher.go` - Showroom namespace watcher (watches user-*-showroom namespaces)
- `src/components/RhaiWorkshopPage.tsx` - Main plugin component (iframes, URL bar with history, postMessage listener, terminal auto-open, paste logic, per-user proxy URL rewriting)
- `console-extensions.json` - Plugin route (`/rhai-workshop`) and nav entry
- `Containerfile` - 3-stage build (UBI9 Node.js + UBI10 Go + UBI9 minimal runtime)
- `gitops/base/` - Base Kubernetes manifests (Namespace, ServiceAccount, ClusterRoles for auth/config/cluster-setup, Deployment, Service, ConsolePlugin with UserToken proxy)
- `gitops/overlays/showroom/` - Showroom overlay (adds namespace watch RBAC + showroomDefaults to workshop-config)
- `Makefile` - Build/push targets
