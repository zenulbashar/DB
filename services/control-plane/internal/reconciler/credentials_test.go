package reconciler

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/store/postgres"
)

// The reconciler replicates the canonical backup-credentials secret into each
// tenant namespace at provision time (ADR-020 — closes the "secret sync"
// pending item the barman spec always depended on).

func canonicalSecret(cfg *BackupConfig) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfg.CredentialsSecret,
			Namespace: cfg.CredentialsSourceNamespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"ACCESS_KEY_ID":     []byte("minio"),
			"ACCESS_SECRET_KEY": []byte("secret"),
		},
	}
}

func TestProvisionReplicatesBackupCredentials(t *testing.T) {
	w := testWork(domain.StateProvisioning, domain.BranchDevelopment)
	src := &fakeSource{work: []postgres.BranchWork{w}}
	e, kc, cfg := newEngineWithBackup(t, src)
	cfg.CredentialsSourceNamespace = PlatformNamespace
	if err := kc.Create(context.Background(), canonicalSecret(cfg)); err != nil {
		t.Fatal(err)
	}

	if err := e.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "Secret"})
	if err := kc.Get(context.Background(),
		client.ObjectKey{Namespace: NamespaceName(w.ProjectID), Name: cfg.CredentialsSecret}, got); err != nil {
		t.Fatalf("replicated secret missing in tenant namespace: %v", err)
	}
	data, _, _ := unstructured.NestedMap(got.Object, "data")
	if len(data) != 2 {
		t.Fatalf("replicated secret data = %v, want the 2 canonical keys", data)
	}
}

// A missing canonical secret fails provisioning loudly (a cluster that cannot
// archive would silently violate R-2), and the branch is not marked ready.
func TestProvisionFailsWithoutCanonicalCredentials(t *testing.T) {
	w := testWork(domain.StateProvisioning, domain.BranchDevelopment)
	src := &fakeSource{work: []postgres.BranchWork{w}}
	e, _, cfg := newEngineWithBackup(t, src)
	cfg.CredentialsSourceNamespace = PlatformNamespace // canonical secret NOT created

	err := e.ReconcileOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "backup credentials") {
		t.Fatalf("expected a backup-credentials error, got %v", err)
	}
	if len(src.ready) != 0 {
		t.Fatalf("branch must not go ready without archive credentials: %v", src.ready)
	}
}

// Replication disabled (empty source namespace) keeps the pre-ADR-020
// behavior: no secret copy, provisioning proceeds (externally-managed secrets).
func TestProvisionSkipsReplicationWhenDisabled(t *testing.T) {
	w := testWork(domain.StateProvisioning, domain.BranchDevelopment)
	src := &fakeSource{work: []postgres.BranchWork{w}}
	e, kc, cfg := newEngineWithBackup(t, src)
	cfg.CredentialsSourceNamespace = "" // default in tests / external management

	if err := e.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "Secret"})
	err := kc.Get(context.Background(),
		client.ObjectKey{Namespace: NamespaceName(w.ProjectID), Name: cfg.CredentialsSecret}, got)
	if err == nil {
		t.Fatal("no secret should be replicated when the source namespace is empty")
	}
}
