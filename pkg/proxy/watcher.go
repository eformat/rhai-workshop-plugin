package proxy

import (
	"context"
	"encoding/json"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
)

var showroomNSPattern = regexp.MustCompile(`^user-(.+)-showroom$`)

type userShowroomData struct {
	guid string
	vars map[string]string
}

// StartShowroomWatcher starts a background goroutine that watches for
// user-*-showroom namespaces, reads showroom-userdata ConfigMaps, and
// populates the SubstitutionStore with per-user substitution rules.
func StartShowroomWatcher(ctx context.Context, clientset kubernetes.Interface, store *SubstitutionStore, namespace string, defaults map[string]string, host string) {
	reconcileFreq := 30 * time.Second
	reconcileCh := make(chan struct{}, 1)

	log.Printf("Starting showroom watcher (namespace: %s, host: %s, reconcile: %s)",
		namespace, host, reconcileFreq)

	// Initial reconciliation
	reconcileShowroom(ctx, clientset, store, defaults, host)

	// Start namespace watcher
	go watchNamespaces(ctx, clientset, reconcileCh)

	// Main loop: debounced watch events + periodic reconciliation
	go func() {
		ticker := time.NewTicker(reconcileFreq)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				log.Println("Showroom watcher shutting down")
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
				reconcileShowroom(ctx, clientset, store, defaults, host)
			case <-ticker.C:
				reconcileShowroom(ctx, clientset, store, defaults, host)
			}
		}
	}()
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
		return err
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return nil
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

func reconcileShowroom(ctx context.Context, clientset kubernetes.Interface, store *SubstitutionStore, defaults map[string]string, host string) {
	log.Println("Reconciling showroom proxy config...")

	if len(defaults) == 0 {
		log.Println("No showroomDefaults configured, clearing proxy config")
		store.SetAllRules(make(map[string][]SubstitutionRule))
		return
	}

	// List all namespaces and filter for user-*-showroom
	nsList, err := clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Printf("Error listing namespaces: %v", err)
		return
	}

	allRules := make(map[string][]SubstitutionRule)
	var userCount int

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

		userData := ParseShowroomData(cm.Data["user_data.yml"])
		if len(userData) == 0 {
			continue
		}

		rules := buildRules(defaults, userData, guid)
		if len(rules) > 0 {
			allRules[guid] = rules
			userCount++
			log.Printf("  Found user %s (%d rules)", guid, len(rules))
		}
	}

	store.SetAllRules(allRules)
	log.Printf("Reconciliation complete: %d user(s) configured", userCount)
}

// buildRules generates substitution rules for a single user by comparing
// default values against the user's actual values. Rules are deduplicated
// by default value (alphabetically first key wins) and sorted longest-first.
func buildRules(defaults, userData map[string]string, guid string) []SubstitutionRule {
	// Sort keys for deterministic iteration order
	keys := make([]string, 0, len(defaults))
	for k := range defaults {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	seen := make(map[string]bool)
	var rules []SubstitutionRule
	for _, key := range keys {
		defaultVal := defaults[key]
		realVal, ok := userData[key]
		if !ok || realVal == defaultVal {
			continue
		}
		// Deduplicate by default value — first key (alphabetically) wins
		if seen[defaultVal] {
			log.Printf("  Warning: duplicate default %q (key %q) skipped for user %s", defaultVal, key, guid)
			continue
		}
		seen[defaultVal] = true
		rules = append(rules, SubstitutionRule{DefaultVal: defaultVal, RealVal: realVal})
	}

	// Sort longest first to avoid partial matches; break ties alphabetically
	sort.Slice(rules, func(i, j int) bool {
		if len(rules[i].DefaultVal) != len(rules[j].DefaultVal) {
			return len(rules[i].DefaultVal) > len(rules[j].DefaultVal)
		}
		return rules[i].DefaultVal < rules[j].DefaultVal
	})

	return rules
}

// ParseShowroomData parses the YAML-like format used by showroom-userdata:
//
//	"key": "value"
func ParseShowroomData(data string) map[string]string {
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

// ParseShowroomOrJSON tries JSON first, then falls back to the YAML-like
// "key": "value" format used by showroom-userdata.
func ParseShowroomOrJSON(data string) map[string]string {
	trimmed := strings.TrimSpace(data)
	if strings.HasPrefix(trimmed, "{") {
		var m map[string]string
		if err := json.Unmarshal([]byte(trimmed), &m); err == nil {
			return m
		}
	}
	return ParseShowroomData(data)
}

// DetectProxyHost extracts the hostname from the first https:// URL in
// the tutorialUrls JSON string.
func DetectProxyHost(tutorialUrls string) string {
	for _, part := range strings.Split(tutorialUrls, "\"") {
		if strings.HasPrefix(part, "https://") {
			u := strings.TrimPrefix(part, "https://")
			if idx := strings.Index(u, "/"); idx > 0 {
				return u[:idx]
			}
		}
	}
	return ""
}
