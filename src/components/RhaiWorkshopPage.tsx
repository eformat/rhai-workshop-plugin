import * as React from 'react';
import Helmet from 'react-helmet';

interface WorkshopConfig {
  openshiftAiUrl: string;
  tutorialUrl: string;
}

const DEFAULT_CONFIG: WorkshopConfig = {
  openshiftAiUrl: 'https://data-science-gateway.apps.ocp.cloud.rhai-tmm.dev/',
  tutorialUrl: 'https://eformat.github.io/voice-agents/voice-agents/index.html',
};

function useWorkshopConfig(): WorkshopConfig {
  const [config, setConfig] = React.useState<WorkshopConfig>(DEFAULT_CONFIG);

  React.useEffect(() => {
    fetch('/api/plugins/rhai-workshop-plugin/config.json')
      .then((res) => {
        if (res.ok) return res.json();
        throw new Error('config not found');
      })
      .then((data: Partial<WorkshopConfig>) => {
        setConfig((prev) => ({ ...prev, ...data }));
      })
      .catch(() => {});
  }, []);

  return config;
}

// Find the xterm textarea in the active terminal tab and paste text into it.
function pasteIntoTerminal(text: string) {
  // Target the active tab panel so paste goes to whichever terminal
  // the user has selected. Inactive panels have the `hidden` attribute.
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

  // Listen for postMessage from the tutorial iframe.
  // When a copy button is clicked in the tutorial, it should send:
  //   window.parent.postMessage({ type: 'copy', text: 'the command' }, '*')
  // We then auto-paste that text into the web terminal.
  React.useEffect(() => {
    const handleMessage = (event: MessageEvent) => {
      if (event.data?.type === 'copy' && typeof event.data.text === 'string') {
        pasteIntoTerminal(event.data.text);
      }
    };
    window.addEventListener('message', handleMessage);
    return () => window.removeEventListener('message', handleMessage);
  }, []);

  // Auto-open the web terminal on page load.
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

  return (
    <>
      <Helmet>
        <title>RHAI Workshop</title>
      </Helmet>
      <div
        style={{
          display: 'flex',
          flexDirection: 'row',
          height: 'calc(100vh - 50px)',
          width: '100%',
        }}
      >
        <iframe
          title="OpenShift AI"
          style={{
            flex: 1,
            border: 'none',
            borderRight: '2px solid #d2d2d2',
            height: '100%',
          }}
          src={config.openshiftAiUrl}
        />
        <iframe
          title="Tutorial"
          style={{
            flex: 1,
            border: 'none',
            height: '100%',
          }}
          src={config.tutorialUrl}
        />
      </div>
    </>
  );
}
