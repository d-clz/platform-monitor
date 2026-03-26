package checker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"
)

// ---- Test helpers ----

// fakeKubeAPI returns an httptest.Server that responds to the three endpoints
// the OCP checker hits. Caller provides the response payloads.
func fakeKubeAPI(
	sas []kubeServiceAccount,
	secrets []kubeSecret,
	rbs []kubeRoleBinding,
) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify auth header is present.
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/api/v1/namespaces/platform-cicd/serviceaccounts":
			json.NewEncoder(w).Encode(kubeList[kubeServiceAccount]{Items: sas})
		case "/api/v1/namespaces/platform-cicd/secrets":
			json.NewEncoder(w).Encode(kubeList[kubeSecret]{Items: secrets})
		case "/apis/rbac.authorization.k8s.io/v1/rolebindings":
			json.NewEncoder(w).Encode(kubeList[kubeRoleBinding]{Items: rbs})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
}

func makeSA(name string) kubeServiceAccount {
	return kubeServiceAccount{
		Metadata: kubeMeta{Name: name, Namespace: "platform-cicd"},
	}
}

func makeSecret(name, saAnnotation, createdAt string) kubeSecret {
	return kubeSecret{
		Metadata: kubeMeta{
			Name:              name,
			Namespace:         "platform-cicd",
			CreationTimestamp: createdAt,
			Annotations:       map[string]string{saAnnotationKey: saAnnotation},
		},
		Type: "kubernetes.io/service-account-token",
	}
}

func makeRoleBinding(namespace, saName, saNamespace, roleName string) kubeRoleBinding {
	return kubeRoleBinding{
		Metadata: kubeMeta{Name: saName + "-binding", Namespace: namespace},
		Subjects: []kubeSubject{{
			Kind:      "ServiceAccount",
			Name:      saName,
			Namespace: saNamespace,
		}},
		RoleRef: kubeRoleRef{Kind: "ClusterRole", Name: roleName},
	}
}

func newChecker(serverURL string, now time.Time) *OCPChecker {
	return &OCPChecker{
		Client:    http.DefaultClient,
		BaseURL:   serverURL,
		Token:     "test-token",
		Namespace: "platform-cicd",
		Now:       func() time.Time { return now },
	}
}

func findApp(results []OCPAppStatus, name string) *OCPAppStatus {
	for i := range results {
		if results[i].Name == name {
			return &results[i]
		}
	}
	return nil
}

// ---- Tests ----

func TestCheck_HappyPath(t *testing.T) {
	now := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)
	created := "2026-03-01T12:00:00Z"

	server := fakeKubeAPI(
		[]kubeServiceAccount{
			makeSA("pfm-image-builder"),
			makeSA("pfm-deployer"),
			makeSA("crm-image-builder"),
			makeSA("crm-deployer"),
			makeSA("default"), // should be ignored
			makeSA("builder"), // should be ignored
		},
		[]kubeSecret{
			makeSecret("pfm-image-builder-token", "pfm-image-builder", created),
			makeSecret("pfm-deployer-token", "pfm-deployer", created),
			makeSecret("crm-image-builder-token", "crm-image-builder", created),
			makeSecret("crm-deployer-token", "crm-deployer", created),
			makeSecret("unrelated-secret", "", created), // no annotation match
		},
		[]kubeRoleBinding{
			makeRoleBinding("pfm-sit", "pfm-image-builder", "platform-cicd", "ci-image-builder"),
			makeRoleBinding("pfm-sit", "pfm-deployer", "platform-cicd", "ci-deployer"),
			makeRoleBinding("pfm-uat", "pfm-image-builder", "platform-cicd", "ci-image-builder"),
			makeRoleBinding("pfm-uat", "pfm-deployer", "platform-cicd", "ci-deployer"),
			makeRoleBinding("crm-sit", "crm-deployer", "platform-cicd", "ci-deployer"),
			// unrelated binding from different namespace
			makeRoleBinding("other-ns", "other-sa", "other-ns", "admin"),
		},
	)
	defer server.Close()

	checker := newChecker(server.URL, now)
	results, err := checker.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d apps, want 2", len(results))
	}

	// Check pfm.
	pfm := findApp(results, "pfm")
	if pfm == nil {
		t.Fatal("pfm not found in results")
	}
	if !pfm.ImageBuilderSA.Exists {
		t.Error("pfm: ImageBuilderSA should exist")
	}
	if !pfm.DeployerSA.Exists {
		t.Error("pfm: DeployerSA should exist")
	}
	if !pfm.ImageBuilderToken.Exists {
		t.Error("pfm: ImageBuilderToken should exist")
	}
	if !pfm.DeployerToken.Exists {
		t.Error("pfm: DeployerToken should exist")
	}

	expectedAge := now.Sub(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))
	if pfm.ImageBuilderToken.Age != expectedAge {
		t.Errorf("pfm: ImageBuilderToken.Age = %v, want %v", pfm.ImageBuilderToken.Age, expectedAge)
	}

	sort.Strings(pfm.Bindings.ImageBuilderNamespaces)
	if len(pfm.Bindings.ImageBuilderNamespaces) != 2 {
		t.Errorf("pfm: ImageBuilderNamespaces = %v, want [pfm-sit, pfm-uat]", pfm.Bindings.ImageBuilderNamespaces)
	}

	// Check crm.
	crm := findApp(results, "crm")
	if crm == nil {
		t.Fatal("crm not found in results")
	}
	if len(crm.Bindings.ImageBuilderNamespaces) != 0 {
		t.Errorf("crm: ImageBuilderNamespaces = %v, want []", crm.Bindings.ImageBuilderNamespaces)
	}
	if len(crm.Bindings.DeployerNamespaces) != 1 || crm.Bindings.DeployerNamespaces[0] != "crm-sit" {
		t.Errorf("crm: DeployerNamespaces = %v, want [crm-sit]", crm.Bindings.DeployerNamespaces)
	}
}

func TestCheck_MissingSA(t *testing.T) {
	now := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)
	created := "2026-03-01T12:00:00Z"

	// pfm only has image-builder SA, no deployer.
	server := fakeKubeAPI(
		[]kubeServiceAccount{
			makeSA("pfm-image-builder"),
		},
		[]kubeSecret{
			makeSecret("pfm-image-builder-token", "pfm-image-builder", created),
		},
		nil,
	)
	defer server.Close()

	checker := newChecker(server.URL, now)
	results, err := checker.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pfm := findApp(results, "pfm")
	if pfm == nil {
		t.Fatal("pfm not found")
	}
	if !pfm.ImageBuilderSA.Exists {
		t.Error("ImageBuilderSA should exist")
	}
	if pfm.DeployerSA.Exists {
		t.Error("DeployerSA should NOT exist")
	}
}

func TestCheck_MissingToken(t *testing.T) {
	now := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)

	// Both SAs exist but no token secrets.
	server := fakeKubeAPI(
		[]kubeServiceAccount{
			makeSA("pfm-image-builder"),
			makeSA("pfm-deployer"),
		},
		[]kubeSecret{}, // no tokens
		nil,
	)
	defer server.Close()

	checker := newChecker(server.URL, now)
	results, err := checker.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pfm := findApp(results, "pfm")
	if pfm == nil {
		t.Fatal("pfm not found")
	}
	if pfm.ImageBuilderToken.Exists {
		t.Error("ImageBuilderToken should NOT exist")
	}
	if pfm.DeployerToken.Exists {
		t.Error("DeployerToken should NOT exist")
	}
}

func TestCheck_NoMatchingSAs(t *testing.T) {
	now := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)

	// Only non-CI service accounts.
	server := fakeKubeAPI(
		[]kubeServiceAccount{
			makeSA("default"),
			makeSA("pipeline-runner"),
		},
		nil,
		nil,
	)
	defer server.Close()

	checker := newChecker(server.URL, now)
	results, err := checker.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results != nil {
		t.Errorf("got %d results, want nil", len(results))
	}
}

func TestCheck_HyphenatedAppName(t *testing.T) {
	now := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)
	created := "2026-03-20T10:00:00Z"

	// App name with hyphens: "my-cool-app".
	server := fakeKubeAPI(
		[]kubeServiceAccount{
			makeSA("my-cool-app-image-builder"),
			makeSA("my-cool-app-deployer"),
		},
		[]kubeSecret{
			makeSecret("my-cool-app-image-builder-token", "my-cool-app-image-builder", created),
		},
		nil,
	)
	defer server.Close()

	checker := newChecker(server.URL, now)
	results, err := checker.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d apps, want 1", len(results))
	}

	app := findApp(results, "my-cool-app")
	if app == nil {
		t.Fatal("my-cool-app not found — suffix stripping may be wrong")
	}
	if !app.ImageBuilderSA.Exists || !app.DeployerSA.Exists {
		t.Error("both SAs should exist")
	}
	if !app.ImageBuilderToken.Exists {
		t.Error("ImageBuilderToken should exist")
	}
	if app.DeployerToken.Exists {
		t.Error("DeployerToken should NOT exist (no secret for it)")
	}
}

func TestCheck_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"forbidden"}`, http.StatusForbidden)
	}))
	defer server.Close()

	checker := newChecker(server.URL, time.Now())
	_, err := checker.Check(context.Background())
	if err == nil {
		t.Fatal("expected error on 403 response")
	}
	if !contains(err.Error(), "403") {
		t.Errorf("error = %q, want it to mention 403", err)
	}
}

func TestCheck_BindingsFromWrongNamespace(t *testing.T) {
	now := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)

	// SA exists in platform-cicd, but the rolebinding references a SA
	// with the same name in a DIFFERENT namespace — should not match.
	server := fakeKubeAPI(
		[]kubeServiceAccount{
			makeSA("pfm-deployer"),
		},
		nil,
		[]kubeRoleBinding{
			makeRoleBinding("pfm-sit", "pfm-deployer", "other-namespace", "ci-deployer"),
		},
	)
	defer server.Close()

	checker := newChecker(server.URL, now)
	results, err := checker.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pfm := findApp(results, "pfm")
	if pfm == nil {
		t.Fatal("pfm not found")
	}
	if len(pfm.Bindings.DeployerNamespaces) != 0 {
		t.Errorf("DeployerNamespaces = %v, want [] (binding was from wrong SA namespace)", pfm.Bindings.DeployerNamespaces)
	}
}

func TestCheck_DuplicateBindingsSameNamespace(t *testing.T) {
	now := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)

	// Two rolebindings in the same namespace for the same SA — should deduplicate.
	server := fakeKubeAPI(
		[]kubeServiceAccount{
			makeSA("pfm-deployer"),
		},
		nil,
		[]kubeRoleBinding{
			makeRoleBinding("pfm-sit", "pfm-deployer", "platform-cicd", "ci-deployer"),
			makeRoleBinding("pfm-sit", "pfm-deployer", "platform-cicd", "ci-deployer-extra"),
		},
	)
	defer server.Close()

	checker := newChecker(server.URL, now)
	results, err := checker.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pfm := findApp(results, "pfm")
	if pfm == nil {
		t.Fatal("pfm not found")
	}
	if len(pfm.Bindings.DeployerNamespaces) != 1 {
		t.Errorf("DeployerNamespaces = %v, want [pfm-sit] (should deduplicate)", pfm.Bindings.DeployerNamespaces)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
