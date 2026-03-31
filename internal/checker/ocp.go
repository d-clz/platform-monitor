// Package checker provides health checks against OCP and GitLab.
package checker

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// ---- Public types ----

// HTTPClient abstracts HTTP calls so we can inject a mock in tests.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// SAStatus reports whether a service account exists.
type SAStatus struct {
	Exists bool
}

// TokenStatus reports whether a token secret exists and its age.
type TokenStatus struct {
	Exists    bool
	CreatedAt time.Time
	Age       time.Duration
}

// BindingInfo describes the rolebindings found for an app.
type BindingInfo struct {
	// ImageBuilderNamespaces lists namespaces where the image-builder SA has a rolebinding.
	ImageBuilderNamespaces []string
	// DeployerNamespaces lists namespaces where the deployer SA has a rolebinding.
	DeployerNamespaces []string
}

// OCPAppStatus is the health snapshot for a single discovered application.
type OCPAppStatus struct {
	Name              string
	ImageBuilderSA    SAStatus
	DeployerSA        SAStatus
	ImageBuilderToken TokenStatus
	DeployerToken     TokenStatus
	Bindings          BindingInfo
}

// ---- Checker ----

const (
	suffixImageBuilder = "-image-builder"
	suffixDeployer     = "-deployer"
	suffixIBToken      = "-image-builder-token"
	suffixDeployToken  = "-deployer-token"

	saAnnotationKey = "kubernetes.io/service-account.name"

	defaultHTTPTimeout = 30 * time.Second
)

// OCPChecker discovers and checks OCP resources for CI service accounts.
type OCPChecker struct {
	Client    HTTPClient
	BaseURL   string // e.g. "https://kubernetes.default.svc"
	Token     string // bearer token for API auth
	Namespace string // SA home namespace, typically "platform-cicd"

	// Now returns the current time. Defaults to time.Now if nil.
	Now func() time.Time
}

// NewOCPChecker constructs an OCPChecker with a pre-configured HTTP client.
// TLS verification is controlled by the OCP_SKIP_TLS environment variable:
// set it to "true" in a ConfigMap env entry for clusters with self-signed certs.
//
//	# ConfigMap
//	data:
//	  OCP_SKIP_TLS: "true"
func NewOCPChecker(baseURL, token, namespace string) *OCPChecker {
	skipTLS := os.Getenv("OCP_SKIP_TLS") == "true"

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		InsecureSkipVerify: skipTLS, //nolint:gosec // intentional, controlled via OCP_SKIP_TLS env var
	}

	return &OCPChecker{
		Client: &http.Client{
			Transport: transport,
			Timeout:   defaultHTTPTimeout,
		},
		BaseURL:   baseURL,
		Token:     token,
		Namespace: namespace,
	}
}

// Check performs a full discovery and health check of all CI service accounts.
func (c *OCPChecker) Check(ctx context.Context) ([]OCPAppStatus, error) {
	now := c.now()

	// Step 1: Discover SAs.
	sas, err := c.listServiceAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing service accounts: %w", err)
	}
	apps := discoverApps(sas)
	if len(apps) == 0 {
		return nil, nil
	}

	// Step 2: Discover token secrets.
	secrets, err := c.listSecrets(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing secrets: %w", err)
	}
	tokenMap := buildTokenMap(secrets, now)

	// Step 3: Discover rolebindings.
	bindings, err := c.listClusterRoleBindings(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing rolebindings: %w", err)
	}
	bindingMap := buildBindingMap(bindings, c.Namespace)

	// Step 4: Assemble results.
	return assembleResults(apps, tokenMap, bindingMap), nil
}

func (c *OCPChecker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// ---- Kube API response types (minimal, only fields we need) ----

type kubeList[T any] struct {
	Items []T `json:"items"`
}

type kubeMeta struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	CreationTimestamp string            `json:"creationTimestamp"`
	Annotations       map[string]string `json:"annotations"`
}

type kubeServiceAccount struct {
	Metadata kubeMeta `json:"metadata"`
}

type kubeSecret struct {
	Metadata kubeMeta `json:"metadata"`
	Type     string   `json:"type"`
}

type kubeRoleBinding struct {
	Metadata kubeMeta      `json:"metadata"`
	Subjects []kubeSubject `json:"subjects"`
	RoleRef  kubeRoleRef   `json:"roleRef"`
}

type kubeSubject struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type kubeRoleRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// ---- API call helpers ----

func (c *OCPChecker) listServiceAccounts(ctx context.Context) ([]kubeServiceAccount, error) {
	path := fmt.Sprintf("/api/v1/namespaces/%s/serviceaccounts", c.Namespace)
	var list kubeList[kubeServiceAccount]
	if err := c.get(ctx, path, &list); err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (c *OCPChecker) listSecrets(ctx context.Context) ([]kubeSecret, error) {
	path := fmt.Sprintf("/api/v1/namespaces/%s/secrets", c.Namespace)
	var list kubeList[kubeSecret]
	if err := c.get(ctx, path, &list); err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (c *OCPChecker) listClusterRoleBindings(ctx context.Context) ([]kubeRoleBinding, error) {
	path := "/apis/rbac.authorization.k8s.io/v1/rolebindings"
	var list kubeList[kubeRoleBinding]
	if err := c.get(ctx, path, &list); err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (c *OCPChecker) get(ctx context.Context, path string, out interface{}) error {
	url := c.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.Client.Do(req)
	if err != nil {
		return fmt.Errorf("executing request to %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API %s returned %d: %s", path, resp.StatusCode, string(body))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding response from %s: %w", path, err)
	}
	return nil
}

// ---- Discovery logic ----

// appEntry tracks which SA roles were discovered for a given app name.
type appEntry struct {
	hasImageBuilder bool
	hasDeployer     bool
}

// discoverApps extracts app names from SAs following the naming convention.
func discoverApps(sas []kubeServiceAccount) map[string]*appEntry {
	apps := make(map[string]*appEntry)

	for _, sa := range sas {
		name := sa.Metadata.Name
		if appName, ok := strings.CutSuffix(name, suffixImageBuilder); ok && appName != "" {
			entry := getOrCreate(apps, appName)
			entry.hasImageBuilder = true
		} else if appName, ok := strings.CutSuffix(name, suffixDeployer); ok && appName != "" {
			entry := getOrCreate(apps, appName)
			entry.hasDeployer = true
		}
	}

	return apps
}

func getOrCreate(m map[string]*appEntry, key string) *appEntry {
	if e, ok := m[key]; ok {
		return e
	}
	e := &appEntry{}
	m[key] = e
	return e
}

// buildTokenMap maps each SA name to its token status using the secret annotation.
func buildTokenMap(secrets []kubeSecret, now time.Time) map[string]TokenStatus {
	result := make(map[string]TokenStatus)

	for _, s := range secrets {
		saName, ok := s.Metadata.Annotations[saAnnotationKey]
		if !ok {
			continue
		}
		// Only consider secrets that match our CI token naming convention.
		secretName := s.Metadata.Name
		if !strings.HasSuffix(secretName, suffixIBToken) && !strings.HasSuffix(secretName, suffixDeployToken) {
			continue
		}

		created, err := time.Parse(time.RFC3339, s.Metadata.CreationTimestamp)
		if err != nil {
			continue
		}

		result[saName] = TokenStatus{
			Exists:    true,
			CreatedAt: created,
			Age:       now.Sub(created),
		}
	}

	return result
}

// bindingEntry groups namespace lists per SA role for an app.
type bindingEntry struct {
	imageBuilderNS []string
	deployerNS     []string
}

// buildBindingMap scans all rolebindings for subjects matching our SA naming pattern.
func buildBindingMap(bindings []kubeRoleBinding, saNamespace string) map[string]*bindingEntry {
	result := make(map[string]*bindingEntry)

	for _, rb := range bindings {
		ns := rb.Metadata.Namespace
		for _, subj := range rb.Subjects {
			if subj.Kind != "ServiceAccount" {
				continue
			}
			if subj.Namespace != saNamespace {
				continue
			}

			if appName, ok := strings.CutSuffix(subj.Name, suffixImageBuilder); ok && appName != "" {
				entry := getOrCreateBinding(result, appName)
				entry.imageBuilderNS = appendUnique(entry.imageBuilderNS, ns)
			} else if appName, ok := strings.CutSuffix(subj.Name, suffixDeployer); ok && appName != "" {
				entry := getOrCreateBinding(result, appName)
				entry.deployerNS = appendUnique(entry.deployerNS, ns)
			}
		}
	}

	return result
}

func getOrCreateBinding(m map[string]*bindingEntry, key string) *bindingEntry {
	if e, ok := m[key]; ok {
		return e
	}
	e := &bindingEntry{}
	m[key] = e
	return e
}

func appendUnique(slice []string, val string) []string {
	for _, s := range slice {
		if s == val {
			return slice
		}
	}
	return append(slice, val)
}

// assembleResults merges the three discovery maps into a sorted slice of statuses.
func assembleResults(apps map[string]*appEntry, tokens map[string]TokenStatus, bindings map[string]*bindingEntry) []OCPAppStatus {
	results := make([]OCPAppStatus, 0, len(apps))

	for name, entry := range apps {
		status := OCPAppStatus{
			Name:           name,
			ImageBuilderSA: SAStatus{Exists: entry.hasImageBuilder},
			DeployerSA:     SAStatus{Exists: entry.hasDeployer},
		}

		// Token lookup by SA name.
		ibSAName := name + suffixImageBuilder
		if tok, ok := tokens[ibSAName]; ok {
			status.ImageBuilderToken = tok
		}

		depSAName := name + suffixDeployer
		if tok, ok := tokens[depSAName]; ok {
			status.DeployerToken = tok
		}

		// Bindings.
		if b, ok := bindings[name]; ok {
			status.Bindings = BindingInfo{
				ImageBuilderNamespaces: b.imageBuilderNS,
				DeployerNamespaces:     b.deployerNS,
			}
		}

		results = append(results, status)
	}

	// Sort for deterministic output — map iteration order is not guaranteed.
	sort.Slice(results, func(i, j int) bool {
		return results[i].Name < results[j].Name
	})

	return results
}
