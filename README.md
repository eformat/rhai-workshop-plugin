# RHAI Workshop Console Plugin

On a 4.10+ OpenShift cluster, deploy this dynamic console plugin.

Dependencies: **rhai-nav-plugin** must be deployed first — it defines the "RHAI" top-level navigation section that this plugin's nav item references.

See [CLAUDE.md](CLAUDE.md) for more details.

## Install

### Base deployment (no showroom proxy)

```bash
oc apply -k ./gitops/base
```

### Showroom proxy overlay (per-user variable substitution)

```bash
oc apply -k ./gitops/overlays/showroom
```

This overlay adds:
- **showroom-proxy-watcher** — a Go service that watches `user-*-showroom` namespaces, reads `showroom-userdata` ConfigMaps, and generates per-user nginx `sub_filter` rules to replace placeholder values with real user credentials in tutorial content served from GitHub Pages.
- **showroom-proxy-conf** — a ConfigMap (managed by the watcher) containing the generated nginx proxy config and a JSON list of active user GUIDs.
- Volume mounts on the plugin deployment to serve proxy config and `proxy-users.json`.

### Manual deployment

```bash
oc process -f template.yaml \
  -p PLUGIN_NAME=rhai-workshop-plugin \
  -p NAMESPACE=rhai-workshop-plugin \
  -p IMAGE=quay.io/eformat/rhai-workshop-plugin:latest \
  | oc create -f -
```

```bash
oc patch consoles.operator.openshift.io cluster \
  --patch '{ "spec": { "plugins": ["rhai-workshop-plugin"] } }' --type=merge
```

## Configuration

### Tutorial URLs

Configure tutorials in the `workshop-config` ConfigMap:

```yaml
kind: ConfigMap
apiVersion: v1
metadata:
  name: workshop-config
  namespace: rhai-workshop-plugin
data:
  tutorialUrls: |
    [
      {"name": "Voice Agents Smart Start", "url": "https://rhpds.github.io/ai-lightning-voice-agents-showroom"},
      {"name": "Word Swarm Smart Start", "url": "https://rhpds.github.io/ai-lightning-wordswarm-showroom"}
    ]
```

Then restart the pod:

```bash
oc rollout restart deployment/rhai-workshop-plugin -n rhai-workshop-plugin
```

### Showroom variable substitution

When using the showroom overlay, add `showroomDefaults` to the `workshop-config` ConfigMap. These are the placeholder values baked into the GitHub Pages Antora build that the proxy will replace with real per-user values:

```yaml
data:
  showroomDefaults: |
    "guid": "abc123"
    "bastion_public_hostname": "bastion.example.com"
    "bastion_ssh_user_name": "lab-user"
    "bastion_ssh_password": "%bastion_ssh_password%"
    "openshift_console_url": "https://console-openshift-console.apps.cluster.example.com"
    "openshift_cluster_ingress_domain": "apps.cluster.example.com"
    "openshift_api_url": "https://api.cluster.example.com:6443"
    "user": "user-abc123"
    "password": "%password%"
```

**Important:** Default values must be unique across all attributes. Attributes that share a default value (e.g. two fields both defaulting to `password`) will conflict in `sub_filter` — use `%placeholder%` patterns to ensure uniqueness. These placeholders must match the corresponding Antora `antora.yml` attribute defaults in the tutorial repos.

The watcher auto-detects the proxy host from the first `https://` URL in `tutorialUrls`. Override with `--proxy-host` flag if needed.

Supports opening the OpenShift Console within the plugin for ease-of-use when you install the [Browser Extension](BROWSER_EXTENSTIONS.md).

![wkshop-plugin-demo.png](wkshop-plugin-demo.png)

## Build

```bash
# Plugin image
make podman-push

# Showroom proxy watcher image
make watcher-push

# Both
make all-push
```
