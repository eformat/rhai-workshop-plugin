package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	pluginNamespace    string
	pluginDeployment   string
	configMapName      string
	workshopConfigName string
	reconcileFreq      time.Duration
	proxyHost          string

	showroomNSPattern = regexp.MustCompile(`^user-(.+)-showroom$`)
)

func main() {
	flag.StringVar(&pluginNamespace, "plugin-namespace", "rhai-workshop-plugin", "Namespace of the workshop plugin deployment")
	flag.StringVar(&pluginDeployment, "plugin-deployment", "rhai-workshop-plugin", "Name of the workshop plugin deployment")
	flag.StringVar(&configMapName, "configmap-name", "showroom-proxy-conf", "Name of the ConfigMap to write proxy config to")
	flag.StringVar(&workshopConfigName, "workshop-config", "workshop-config", "Name of the workshop-config ConfigMap with showroomDefaults")
	flag.StringVar(&proxyHost, "proxy-host", "", "GitHub Pages host to proxy (auto-detected from tutorialUrls if empty)")
	flag.DurationVar(&reconcileFreq, "reconcile-frequency", 30*time.Second, "How often to reconcile all namespaces")
	flag.Parse()

	log.Printf("Showroom proxy watcher starting (namespace: %s, deployment: %s, reconcile: %s)",
		pluginNamespace, pluginDeployment, reconcileFreq)

	config, err := buildKubeConfig()
	if err != nil {
		log.Fatalf("Failed to build kubeconfig: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("Failed to create kubernetes client: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down", sig)
		cancel()
	}()

	// Channel for debounced reconcile triggers from the watcher
	reconcileCh := make(chan struct{}, 1)

	// Initial reconciliation
	reconcile(ctx, clientset)

	// Start namespace watcher
	go watchNamespaces(ctx, clientset, reconcileCh)

	// Main loop: debounced watch events + periodic reconciliation
	ticker := time.NewTicker(reconcileFreq)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("Shutting down")
			return
		case <-reconcileCh:
			// Debounce: wait briefly for more events to accumulate
			time.Sleep(2 * time.Second)
			// Drain any queued events
			for {
				select {
				case <-reconcileCh:
				default:
					goto drained
				}
			}
		drained:
			reconcile(ctx, clientset)
		case <-ticker.C:
			reconcile(ctx, clientset)
		}
	}
}

func buildKubeConfig() (*rest.Config, error) {
	config, err := rest.InClusterConfig()
	if err == nil {
		return config, nil
	}
	kubeconfigPath := os.Getenv("KUBECONFIG")
	if kubeconfigPath == "" {
		home, _ := os.UserHomeDir()
		kubeconfigPath = home + "/.kube/config"
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfigPath)
}

func watchNamespaces(ctx context.Context, clientset kubernetes.Interface, reconcileCh chan<- struct{}) {
	for {
		if ctx.Err() != nil {
			return
		}
		log.Println("Starting namespace watcher...")
		if err := runNamespaceWatch(ctx, clientset, reconcileCh); err != nil {
			log.Printf("Namespace watcher error: %v, reconnecting in 5s...", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func runNamespaceWatch(ctx context.Context, clientset kubernetes.Interface, reconcileCh chan<- struct{}) error {
	watcher, err := clientset.CoreV1().Namespaces().Watch(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("creating watch: %w", err)
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return fmt.Errorf("watch channel closed")
			}
			ns, ok := event.Object.(*corev1.Namespace)
			if !ok {
				continue
			}
			if !showroomNSPattern.MatchString(ns.Name) {
				continue
			}

			switch event.Type {
			case watch.Added, watch.Modified, watch.Deleted:
				log.Printf("[watch] Namespace %s %s, queuing reconcile", ns.Name, event.Type)
				select {
				case reconcileCh <- struct{}{}:
				default: // already queued
				}
			}
		}
	}
}

type userShowroomData struct {
	guid string
	vars map[string]string
}

func reconcile(ctx context.Context, clientset kubernetes.Interface) {
	log.Println("Reconciling showroom proxy config...")

	// Read showroomDefaults from workshop-config
	defaults, detectedHost, err := readShowroomDefaults(ctx, clientset)
	if err != nil {
		log.Printf("Error reading showroom defaults: %v", err)
		return
	}
	if len(defaults) == 0 {
		log.Println("No showroomDefaults in workshop-config, writing empty proxy config")
		writeEmptyConfig(ctx, clientset)
		return
	}

	effectiveHost := proxyHost
	if effectiveHost == "" {
		effectiveHost = detectedHost
	}
	if effectiveHost == "" {
		effectiveHost = "rhpds.github.io"
	}

	// List all namespaces and filter for user-*-showroom
	nsList, err := clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Printf("Error listing namespaces: %v", err)
		return
	}

	var users []userShowroomData
	for _, ns := range nsList.Items {
		matches := showroomNSPattern.FindStringSubmatch(ns.Name)
		if matches == nil {
			continue
		}
		if ns.Status.Phase != corev1.NamespaceActive {
			continue
		}
		guid := matches[1]

		cm, err := clientset.CoreV1().ConfigMaps(ns.Name).Get(ctx, "showroom-userdata", metav1.GetOptions{})
		if err != nil {
			if !k8serrors.IsNotFound(err) {
				log.Printf("Error reading showroom-userdata in %s: %v", ns.Name, err)
			}
			continue
		}

		userData := parseShowroomData(cm.Data["user_data.yml"])
		if len(userData) > 0 {
			users = append(users, userShowroomData{guid: guid, vars: userData})
			log.Printf("  Found user %s (%d vars)", guid, len(userData))
		}
	}

	// Generate proxy config
	proxyConf := generateProxyConf(users, defaults, effectiveHost)
	proxyUsers := generateProxyUsers(users)

	// Update ConfigMap (returns true if content changed)
	changed, err := upsertProxyConfigMap(ctx, clientset, proxyConf, proxyUsers)
	if err != nil {
		log.Printf("Error updating proxy ConfigMap: %v", err)
		return
	}

	if changed {
		if err := rollDeployment(ctx, clientset); err != nil {
			log.Printf("Error rolling deployment: %v", err)
			return
		}
	}

	log.Printf("Reconciliation complete: %d user(s) configured", len(users))
}

func readShowroomDefaults(ctx context.Context, clientset kubernetes.Interface) (map[string]string, string, error) {
	cm, err := clientset.CoreV1().ConfigMaps(pluginNamespace).Get(ctx, workshopConfigName, metav1.GetOptions{})
	if err != nil {
		return nil, "", fmt.Errorf("getting workshop-config: %w", err)
	}

	defaults := parseShowroomOrJSON(cm.Data["showroomDefaults"])

	// Auto-detect proxy host from tutorialUrls
	var host string
	if urls := cm.Data["tutorialUrls"]; urls != "" {
		for _, part := range strings.Split(urls, "\"") {
			if strings.HasPrefix(part, "https://") {
				u := strings.TrimPrefix(part, "https://")
				if idx := strings.Index(u, "/"); idx > 0 {
					host = u[:idx]
				}
				break
			}
		}
	}

	return defaults, host, nil
}

// parseShowroomData parses the YAML-like format used by showroom-userdata:
//
//	"key": "value"
func parseShowroomData(data string) map[string]string {
	result := make(map[string]string)
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.Trim(strings.TrimSpace(parts[0]), "\"")
		val := strings.Trim(strings.TrimSpace(parts[1]), "\"")
		if key != "" {
			result[key] = val
		}
	}
	return result
}

// parseShowroomOrJSON tries JSON first, then falls back to the YAML-like
// "key": "value" format used by showroom-userdata.
func parseShowroomOrJSON(data string) map[string]string {
	trimmed := strings.TrimSpace(data)
	if strings.HasPrefix(trimmed, "{") {
		var m map[string]string
		if err := json.Unmarshal([]byte(trimmed), &m); err == nil {
			return m
		}
	}
	return parseShowroomData(data)
}

type subFilterRule struct {
	defaultVal string
	realVal    string
}

func generateProxyConf(users []userShowroomData, defaults map[string]string, host string) string {
	if len(users) == 0 {
		return "# No showroom users found\n"
	}

	var b strings.Builder
	b.WriteString("resolver 8.8.8.8 valid=30s ipv6=off;\n\n")

	for _, user := range users {
		// Sort keys for deterministic iteration order
		keys := make([]string, 0, len(defaults))
		for k := range defaults {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		seen := make(map[string]bool)
		var rules []subFilterRule
		for _, key := range keys {
			defaultVal := defaults[key]
			realVal, ok := user.vars[key]
			if !ok || realVal == defaultVal {
				continue
			}
			// Deduplicate by default value — first key (alphabetically) wins
			if seen[defaultVal] {
				log.Printf("  Warning: duplicate default %q (key %q) skipped for user %s", defaultVal, key, user.guid)
				continue
			}
			seen[defaultVal] = true
			rules = append(rules, subFilterRule{defaultVal: defaultVal, realVal: realVal})
		}
		// Sort longest first to avoid partial matches; break ties alphabetically for stable output
		sort.Slice(rules, func(i, j int) bool {
			if len(rules[i].defaultVal) != len(rules[j].defaultVal) {
				return len(rules[i].defaultVal) > len(rules[j].defaultVal)
			}
			return rules[i].defaultVal < rules[j].defaultVal
		})

		b.WriteString(fmt.Sprintf("location /tutorial-proxy/%s/ {\n", user.guid))
		b.WriteString(fmt.Sprintf("    proxy_pass https://%s/;\n", host))
		b.WriteString("    proxy_ssl_server_name on;\n")
		b.WriteString(fmt.Sprintf("    proxy_set_header Host %s;\n", host))
		b.WriteString("    proxy_set_header Accept-Encoding \"\";\n")
		b.WriteString("    gunzip on;\n")
		b.WriteString("    proxy_hide_header Permissions-Policy;\n")
		b.WriteString("    add_header Permissions-Policy \"clipboard-read=*, clipboard-write=*\" always;\n")
		b.WriteString("    sub_filter_once off;\n")
		b.WriteString("    sub_filter_types text/html text/css application/javascript;\n")
		// Inject clipboard fallback for browsers that block Clipboard API in iframes
		b.WriteString("    sub_filter '</head>' '<script>")
		b.WriteString("(function(){if(!navigator.clipboard)return;")
		b.WriteString("var o=navigator.clipboard.writeText.bind(navigator.clipboard);")
		b.WriteString("navigator.clipboard.writeText=function(t){")
		b.WriteString("return o(t).catch(function(){")
		b.WriteString("var a=document.createElement(\"textarea\");")
		b.WriteString("a.value=t;a.style.position=\"fixed\";a.style.left=\"-9999px\";")
		b.WriteString("document.body.appendChild(a);a.select();")
		b.WriteString("document.execCommand(\"copy\");")
		b.WriteString("document.body.removeChild(a);")
		b.WriteString("return Promise.resolve();")
		b.WriteString("});};")
		b.WriteString("})();</script></head>';\n")
		for _, rule := range rules {
			b.WriteString(fmt.Sprintf("    sub_filter '%s' '%s';\n", rule.defaultVal, rule.realVal))
		}
		b.WriteString("}\n\n")
	}

	return b.String()
}

func generateProxyUsers(users []userShowroomData) string {
	guids := make([]string, 0, len(users))
	for _, u := range users {
		guids = append(guids, u.guid)
	}
	data, _ := json.Marshal(guids)
	return string(data)
}

func upsertProxyConfigMap(ctx context.Context, clientset kubernetes.Interface, proxyConf, proxyUsers string) (bool, error) {
	cmData := map[string]string{
		"proxy.conf":       proxyConf,
		"proxy-users.json": proxyUsers,
	}

	existing, err := clientset.CoreV1().ConfigMaps(pluginNamespace).Get(ctx, configMapName, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      configMapName,
				Namespace: pluginNamespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "showroom-proxy-watcher",
				},
			},
			Data: cmData,
		}
		_, err := clientset.CoreV1().ConfigMaps(pluginNamespace).Create(ctx, cm, metav1.CreateOptions{})
		if err != nil {
			return false, fmt.Errorf("creating ConfigMap: %w", err)
		}
		log.Println("Created proxy ConfigMap")
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("getting ConfigMap: %w", err)
	}

	// Skip update if content unchanged
	if existing.Data["proxy.conf"] == proxyConf && existing.Data["proxy-users.json"] == proxyUsers {
		log.Println("Proxy config unchanged, skipping update")
		return false, nil
	}

	existing.Data = cmData
	_, err = clientset.CoreV1().ConfigMaps(pluginNamespace).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return false, fmt.Errorf("updating ConfigMap: %w", err)
	}
	log.Println("Updated proxy ConfigMap")
	return true, nil
}

func rollDeployment(ctx context.Context, clientset kubernetes.Interface) error {
	patch := fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"showroom-proxy/restartedAt":"%s"}}}}}`,
		time.Now().Format(time.RFC3339),
	)
	_, err := clientset.AppsV1().Deployments(pluginNamespace).Patch(
		ctx, pluginDeployment, types.MergePatchType, []byte(patch), metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("patching deployment: %w", err)
	}
	log.Println("Triggered rolling restart of plugin deployment")
	return nil
}

func writeEmptyConfig(ctx context.Context, clientset kubernetes.Interface) {
	if _, err := upsertProxyConfigMap(ctx, clientset, "# No showroom defaults configured\n", "[]"); err != nil {
		log.Printf("Error writing empty config: %v", err)
	}
}
