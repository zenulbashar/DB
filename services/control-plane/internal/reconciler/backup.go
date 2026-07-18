package reconciler

import (
	"fmt"
	"hash/fnv"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/zenulbashar/DB/services/control-plane/internal/store/postgres"
)

var ScheduledBackupGVK = schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "ScheduledBackup"}

// BackupConfig points WAL archiving and base backups at object storage
// (DATABASE_ARCHITECTURE §2/§3). When nil, clusters build without a backup
// section — acceptable only in local dev; staging/prod set it always.
type BackupConfig struct {
	// EndpointURL of the S3-compatible store ("" for real AWS S3).
	EndpointURL string
	// BucketBase, e.g. "s3://ndb-syd1-wal" — per-project/branch prefixes are
	// appended (bucket layout per DATABASE_ARCHITECTURE §2).
	BucketBase string
	// CredentialsSecret is the k8s Secret (in each project namespace,
	// replicated by the reconciler's secret sync — Phase 2 pending item)
	// holding ACCESS_KEY_ID / ACCESS_SECRET_KEY.
	CredentialsSecret string
}

// destinationPath isolates each branch's WAL/backup stream.
func (b *BackupConfig) destinationPath(w postgres.BranchWork) string {
	return fmt.Sprintf("%s/%s/%s", b.BucketBase, w.ProjectID, w.Branch.ID)
}

// barmanSection renders spec.backup for a Cluster.
func (b *BackupConfig) barmanSection(w postgres.BranchWork) map[string]any {
	s3Creds := map[string]any{
		"accessKeyId": map[string]any{
			"name": b.CredentialsSecret, "key": "ACCESS_KEY_ID",
		},
		"secretAccessKey": map[string]any{
			"name": b.CredentialsSecret, "key": "ACCESS_SECRET_KEY",
		},
	}
	obj := map[string]any{
		"destinationPath": b.destinationPath(w),
		"s3Credentials":   s3Creds,
		"wal": map[string]any{
			"compression": "gzip",
			// Client-side encryption of archived WAL/backups is layered on
			// top of bucket SSE (SECURITY_MODEL §5 "at rest").
		},
		"data": map[string]any{"compression": "gzip"},
	}
	if b.EndpointURL != "" {
		obj["endpointURL"] = b.EndpointURL
	}
	// Guard against a zero-valued branch record: "0d" retention would mean
	// no PITR window at all. 7 days is the documented floor
	// (DATABASE_ARCHITECTURE §3).
	retention := w.Branch.RetentionDays
	if retention < 1 {
		retention = 7
	}
	return map[string]any{
		"barmanObjectStore": obj,
		// CNPG retention policy prunes base backups + WAL beyond the branch's
		// PITR window.
		"retentionPolicy": fmt.Sprintf("%dd", retention),
	}
}

// BuildScheduledBackup renders the nightly base backup for a branch
// (DATABASE_ARCHITECTURE §3). The minute/hour are spread deterministically by
// branch ID so a cell's backups don't stampede the object store at midnight.
func BuildScheduledBackup(w postgres.BranchWork) *unstructured.Unstructured {
	h := fnv.New32a()
	h.Write([]byte(w.Branch.ID))
	minute := h.Sum32() % 60
	hour := 1 + h.Sum32()%4 // 01:00–04:59 local cell time
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": ScheduledBackupGVK.GroupVersion().String(),
		"kind":       ScheduledBackupGVK.Kind,
		"metadata": map[string]any{
			"name":      ClusterName(w.Branch.ID) + "-nightly",
			"namespace": NamespaceName(w.ProjectID),
			"labels":    commonLabels(w),
		},
		"spec": map[string]any{
			// CNPG cron format includes seconds.
			"schedule":             fmt.Sprintf("0 %d %d * * *", minute, hour),
			"backupOwnerReference": "self",
			"cluster":              map[string]any{"name": ClusterName(w.Branch.ID)},
		},
	}}
}

// RecoveryTarget selects the point a recovery cluster replays to; zero value
// means "latest".
type RecoveryTarget struct {
	Time time.Time // replay to this timestamp when non-zero
}

// BuildRecoveryCluster renders a Cluster bootstrapped from a source branch's
// WAL archive. Three consumers share this shape (DATABASE_ARCHITECTURE §3/§6):
// the nightly restore-verification job (scratch namespace), instant restore,
// and branch-from-point-in-time (Phase 4).
func BuildRecoveryCluster(source postgres.BranchWork, target RecoveryTarget, cfg *BackupConfig, name, namespace string) *unstructured.Unstructured {
	base := BuildCluster(source, nil)
	base.SetName(name)
	base.SetNamespace(namespace)
	spec := base.Object["spec"].(map[string]any)

	recovery := map[string]any{"source": "origin"}
	if !target.Time.IsZero() {
		recovery["recoveryTarget"] = map[string]any{
			"targetTime": target.Time.UTC().Format(time.RFC3339),
		}
	}
	spec["bootstrap"] = map[string]any{"recovery": recovery}

	origin := map[string]any{
		"name": "origin",
		"barmanObjectStore": map[string]any{
			"destinationPath": cfg.destinationPath(source),
			"s3Credentials": map[string]any{
				"accessKeyId": map[string]any{
					"name": cfg.CredentialsSecret, "key": "ACCESS_KEY_ID",
				},
				"secretAccessKey": map[string]any{
					"name": cfg.CredentialsSecret, "key": "ACCESS_SECRET_KEY",
				},
			},
			"wal": map[string]any{"compression": "gzip"},
		},
	}
	if cfg.EndpointURL != "" {
		origin["barmanObjectStore"].(map[string]any)["endpointURL"] = cfg.EndpointURL
	}
	spec["externalClusters"] = []any{origin}

	// A recovery clone must never archive into its source's WAL stream.
	delete(spec, "backup")
	return base
}
