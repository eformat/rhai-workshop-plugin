# Browser Extensions — RHAI Console Iframe Unblocker

The OpenShift console sets `X-Frame-Options: DENY` and `Content-Security-Policy: frame-ancestors 'none'` which prevents it from being embedded in an iframe. These browser extensions strip those headers for console iframe requests only, allowing the RHAI Workshop plugin to embed the console in the left pane.

You download the browser extension using the download button next to the URL bar within the plugin once it is installed.

## Chrome

1. Open `chrome://extensions/`
2. Enable **Developer mode** (toggle top-right)
3. Click **Load unpacked**
4. Select the `chrome-extension/` directory
5. Navigate to the workshop plugin and enter the console URL in the left iframe's URL bar

## Firefox

1. Open `about:debugging#/runtime/this-firefox`
2. Click **Load Temporary Add-on**
3. Select `firefox-extension/manifest.json`
4. Navigate to the workshop plugin and enter the console URL in the left iframe's URL bar

Note: Temporary add-ons in Firefox are removed when the browser closes. For persistent installation, the extension would need to be signed via [addons.mozilla.org](https://addons.mozilla.org/).

## Configuration

Both extensions are pre-configured for the console hostname which is generated for just the cluster you are running in:

```
console-openshift-console.apps.cluster-name.example.com
```

To add other cluster consoles, edit `IFRAME_HOSTS` in the respective `background.js` file and reload the extension.

## How it works

The extensions intercept HTTP responses for `sub_frame` (iframe) requests to the configured console hostnames and strip the `X-Frame-Options` and `Content-Security-Policy` response headers before the browser enforces them. Regular browsing is unaffected — only iframe loads of the console are modified.
