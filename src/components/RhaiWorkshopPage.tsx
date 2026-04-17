import * as React from 'react';
import Helmet from 'react-helmet';
import {
  Tabs,
  Tab,
  TabTitleText,
  Button,
  SearchInput,
} from '@patternfly/react-core';
import AngleDownIcon from '@patternfly/react-icons/dist/esm/icons/angle-down-icon';
import AngleUpIcon from '@patternfly/react-icons/dist/esm/icons/angle-up-icon';
import SyncAltIcon from '@patternfly/react-icons/dist/esm/icons/sync-alt-icon';

interface TutorialEntry {
  name: string;
  url: string;
}

interface WorkshopConfig {
  openshiftAiUrl: string;
  tutorialUrls: TutorialEntry[];
  showTabs: boolean;
}

const DEFAULT_CONFIG: WorkshopConfig = {
  openshiftAiUrl: 'https://data-science-gateway.apps.ocp.cloud.rhai-tmm.dev/',
  tutorialUrls: [
    { name: 'Voice Agents', url: 'https://eformat.github.io/voice-agents/voice-agents/index.html' },
    { name: 'Rainforest', url: 'https://eformat.github.io/rainforest-docs' },
  ],
  showTabs: true,
};

const PLUGIN_BASE = '/api/plugins/rhai-workshop-plugin';

// Detect the logged-in user's guid and rewrite tutorial URLs through the
// per-user showroom proxy when the watcher has configured one.
function rewriteTutorialUrls(urls: TutorialEntry[], guid: string): TutorialEntry[] {
  return urls.map((entry) => {
    const match = entry.url.match(/^https:\/\/([^/]+)(\/.*)/);
    if (!match) return entry;
    let path = match[2];
    // Ensure directory-style paths end with / to avoid 301 redirects that
    // break relative URL resolution through the console proxy chain.
    if (!path.endsWith('/') && !path.includes('.')) {
      path += '/';
    }
    return {
      ...entry,
      url: `${PLUGIN_BASE}/tutorial-proxy/${guid}${path}`,
    };
  });
}

function useCurrentUserGuid(): string | null {
  const [guid, setGuid] = React.useState<string | null>(null);

  React.useEffect(() => {
    // Fetch proxy-users.json (served by the plugin's own nginx)
    // and the current OpenShift user identity in parallel.
    Promise.all([
      fetch(`${PLUGIN_BASE}/proxy-users.json`)
        .then((r) => (r.ok ? r.json() : []))
        .catch(() => []) as Promise<string[]>,
      fetch('/api/kubernetes/apis/user.openshift.io/v1/users/~')
        .then((r) => (r.ok ? r.json() : null))
        .catch(() => null) as Promise<any>,
    ]).then(([proxyUsers, user]) => {
      if (!user?.metadata?.name || !Array.isArray(proxyUsers) || proxyUsers.length === 0) return;
      // Username format: "user-<guid>" → extract guid
      const username: string = user.metadata.name;
      const m = username.match(/^user-(.+)$/);
      if (m && proxyUsers.includes(m[1])) {
        setGuid(m[1]);
      }
    });
  }, []);

  return guid;
}

function useWorkshopConfig(): WorkshopConfig {
  const [config, setConfig] = React.useState<WorkshopConfig>(DEFAULT_CONFIG);
  const guid = useCurrentUserGuid();

  React.useEffect(() => {
    fetch(`${PLUGIN_BASE}/config.json`)
      .then((res) => {
        if (res.ok) return res.json();
        throw new Error('config not found');
      })
      .then((data: any) => {
        const merged: Partial<WorkshopConfig> = {};
        if (data.openshiftAiUrl) merged.openshiftAiUrl = data.openshiftAiUrl;
        if (Array.isArray(data.tutorialUrls)) merged.tutorialUrls = data.tutorialUrls;
        if (typeof data.showTabs === 'boolean') merged.showTabs = data.showTabs;
        setConfig((prev) => ({ ...prev, ...merged }));
      })
      .catch(() => {});
  }, []);

  // When we know the user's guid and the proxy is available, rewrite URLs
  return React.useMemo(() => {
    if (!guid) return config;
    return { ...config, tutorialUrls: rewriteTutorialUrls(config.tutorialUrls, guid) };
  }, [config, guid]);
}

// Find the xterm textarea in the active terminal tab and paste text into it.
function pasteIntoTerminal(text: string) {
  const activePanel = document.querySelector(
    'section[role="tabpanel"]:not([hidden])',
  );
  const textarea = (activePanel || document).querySelector(
    'textarea.xterm-helper-textarea',
  ) as HTMLTextAreaElement | null;

  if (textarea) {
    textarea.focus();
    const dt = new DataTransfer();
    dt.setData('text/plain', text);
    const pasteEvent = new ClipboardEvent('paste', {
      bubbles: true,
      cancelable: true,
      clipboardData: dt,
    });
    textarea.dispatchEvent(pasteEvent);
  }
}

export default function RhaiWorkshopPage() {
  const config = useWorkshopConfig();
  const [leftWidth, setLeftWidth] = React.useState(50);
  const [activeTab, setActiveTab] = React.useState(0);
  const [urlHistory, setUrlHistory] = React.useState<string[]>([config.openshiftAiUrl]);
  const [historyIndex, setHistoryIndex] = React.useState(0);
  const [urlInput, setUrlInput] = React.useState(config.openshiftAiUrl);
  const [urlBarOpen, setUrlBarOpen] = React.useState(false);
  const [leftKey, setLeftKey] = React.useState(0);
  const userNavigated = React.useRef(false);
  const containerRef = React.useRef<HTMLDivElement>(null);
  const dragging = React.useRef(false);

  const leftUrl = urlHistory[historyIndex] || config.openshiftAiUrl;

  // Update from config when async fetch completes, unless user has manually navigated
  React.useEffect(() => {
    if (config.openshiftAiUrl && !userNavigated.current) {
      setUrlHistory([config.openshiftAiUrl]);
      setHistoryIndex(0);
      setUrlInput(config.openshiftAiUrl);
    }
  }, [config.openshiftAiUrl]);

  const navigateLeft = (input?: string) => {
    let url = (input || urlInput).trim();
    if (url && !/^https?:\/\//i.test(url)) {
      url = 'https://' + url;
    }
    if (url && url !== leftUrl) {
      userNavigated.current = true;
      const newHistory = urlHistory.slice(0, historyIndex + 1);
      newHistory.push(url);
      setUrlHistory(newHistory);
      setHistoryIndex(newHistory.length - 1);
      setUrlInput(url);
    }
  };

  const goBack = () => {
    if (historyIndex > 0) {
      const newIndex = historyIndex - 1;
      setHistoryIndex(newIndex);
      setUrlInput(urlHistory[newIndex]);
    }
  };

  const goForward = () => {
    if (historyIndex < urlHistory.length - 1) {
      const newIndex = historyIndex + 1;
      setHistoryIndex(newIndex);
      setUrlInput(urlHistory[newIndex]);
    }
  };

  React.useEffect(() => {
    const handleMessage = (event: MessageEvent) => {
      if (event.data?.type === 'copy' && typeof event.data.text === 'string') {
        pasteIntoTerminal(event.data.text);
        // Also copy to system clipboard from the top-level context where it's allowed
        navigator.clipboard?.writeText(event.data.text).catch(() => {});
      }
    };
    window.addEventListener('message', handleMessage);
    return () => window.removeEventListener('message', handleMessage);
  }, []);

  React.useEffect(() => {
    const openTerminal = () => {
      const btn = document.querySelector(
        'button[data-quickstart-id="qs-masthead-cloudshell"]',
      ) as HTMLButtonElement | null;
      if (btn) {
        const terminalFrame = document.querySelector(
          'iframe[title="Command line terminal"]',
        );
        if (!terminalFrame) {
          btn.click();
        }
      }
    };
    const timer = setTimeout(openTerminal, 1500);
    return () => clearTimeout(timer);
  }, []);

  const onMouseDown = React.useCallback(() => {
    dragging.current = true;
  }, []);

  React.useEffect(() => {
    const onMouseMove = (e: MouseEvent) => {
      if (!dragging.current || !containerRef.current) return;
      const rect = containerRef.current.getBoundingClientRect();
      const pct = ((e.clientX - rect.left) / rect.width) * 100;
      setLeftWidth(Math.min(80, Math.max(20, pct)));
    };
    const onMouseUp = () => {
      dragging.current = false;
    };
    document.addEventListener('mousemove', onMouseMove);
    document.addEventListener('mouseup', onMouseUp);
    return () => {
      document.removeEventListener('mousemove', onMouseMove);
      document.removeEventListener('mouseup', onMouseUp);
    };
  }, []);

  const showTabs = config.showTabs && config.tutorialUrls.length > 1;
  const currentUrl = config.tutorialUrls[activeTab]?.url || '';

  return (
    <>
      <Helmet>
        <title>RHAI Workshop</title>
      </Helmet>
      <div
        ref={containerRef}
        style={{
          display: 'flex',
          flexDirection: 'row',
          height: 'calc(100vh - 50px)',
          width: '100%',
        }}
      >
        {/* Left pane: collapsible URL bar + iframe */}
        <div
          style={{
            width: `${leftWidth}%`,
            display: 'flex',
            flexDirection: 'column',
            height: '100%',
          }}
        >
          {/* URL bar toggle + input */}
          <div style={{ flexShrink: 0, display: 'flex', alignItems: 'center' }}>
            <Button
              variant="plain"
              onClick={() => setUrlBarOpen((prev) => !prev)}
              aria-label={urlBarOpen ? 'Collapse URL bar' : 'Expand URL bar'}
              style={{ padding: '4px 8px' }}
            >
              {urlBarOpen ? <AngleUpIcon /> : <AngleDownIcon />}
            </Button>
            {urlBarOpen && (
              <>
                <SearchInput
                  style={{ flex: 1, marginRight: '4px' }}
                  aria-label="URL"
                  placeholder="https://..."
                  value={urlInput}
                  onChange={(_e, val) => setUrlInput(val)}
                  onSearch={(_e, val) => navigateLeft(val)}
                  onClear={() => {
                    setUrlInput('');
                  }}
                  onPreviousClick={goBack}
                  onNextClick={goForward}
                  isPreviousNavigationButtonDisabled={historyIndex <= 0}
                  isNextNavigationButtonDisabled={historyIndex >= urlHistory.length - 1}
                  previousNavigationButtonAriaLabel="Back"
                  nextNavigationButtonAriaLabel="Forward"
                  resultsCount={`${historyIndex + 1} / ${urlHistory.length}`}
                  submitSearchButtonLabel="Go"
                />
                <Button
                  variant="plain"
                  onClick={() => setLeftKey((k) => k + 1)}
                  aria-label="Refresh"
                  style={{ padding: '4px 8px' }}
                >
                  <SyncAltIcon />
                </Button>
              </>
            )}
          </div>
          <iframe
            key={leftKey}
            title="OpenShift AI"
            style={{
              flex: 1,
              border: 'none',
              width: '100%',
              pointerEvents: dragging.current ? 'none' : 'auto',
            }}
            src={leftUrl}
          />
        </div>
        {/* Resizable splitter */}
        <div
          onMouseDown={onMouseDown}
          style={{
            width: '6px',
            cursor: 'col-resize',
            backgroundColor: '#d2d2d2',
            flexShrink: 0,
            position: 'relative',
          }}
        >
          <div
            style={{
              position: 'absolute',
              top: '50%',
              left: '50%',
              transform: 'translate(-50%, -50%)',
              width: '2px',
              height: '24px',
              borderLeft: '1px solid #8a8d90',
              borderRight: '1px solid #8a8d90',
            }}
          />
        </div>
        {/* Right pane: tabs + tutorial iframe */}
        <div
          style={{
            width: `${100 - leftWidth}%`,
            display: 'flex',
            flexDirection: 'column',
            height: '100%',
          }}
        >
          {showTabs && (
            <Tabs
              activeKey={activeTab}
              onSelect={(_e, key) => setActiveTab(key as number)}
              isBox={false}
              style={{ flexShrink: 0 }}
            >
              {config.tutorialUrls.map((entry, idx) => (
                <Tab
                  key={idx}
                  eventKey={idx}
                  title={<TabTitleText>{entry.name}</TabTitleText>}
                />
              ))}
            </Tabs>
          )}
          <iframe
            title="Tutorial"
            allow="clipboard-read; clipboard-write"
            style={{
              flex: 1,
              border: 'none',
              width: '100%',
              pointerEvents: dragging.current ? 'none' : 'auto',
            }}
            src={currentUrl}
          />
        </div>
      </div>
    </>
  );
}
