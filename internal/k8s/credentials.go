package k8s

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// DBCredentialRef points to a K8s Secret key that holds a DB credential.
type DBCredentialRef struct {
	Namespace  string
	SecretName string
	Key        string
}

func (r DBCredentialRef) SecretRefString() string {
	return fmt.Sprintf("k8s-secret://%s/%s#%s", r.Namespace, r.SecretName, r.Key)
}

// FoundCredentials holds the discovered credential refs for a single DB instance.
type FoundCredentials struct {
	Namespace string
	PodName   string
	Password  *DBCredentialRef
	User      *DBCredentialRef
	Name      *DBCredentialRef
	Host      *DBCredentialRef
}

// passwordKeys are Secret keys that likely contain a Postgres password.
var passwordKeys = []string{
	"POSTGRES_PASSWORD", "postgresql-password", "password",
	"PGPASSWORD", "DB_PASSWORD", "DATABASE_PASSWORD",
}

// userKeys are Secret keys that likely contain a Postgres username.
var userKeys = []string{
	"POSTGRES_USER", "PGUSER", "DB_USER", "DATABASE_USER", "username",
}

// nameKeys are Secret keys that likely contain a Postgres database name.
var nameKeys = []string{
	"POSTGRES_DB", "PGDATABASE", "DB_NAME", "DATABASE_NAME",
}

// hostKeys are Secret keys that likely contain a Postgres host.
var hostKeys = []string{
	"POSTGRES_HOST", "PGHOST", "DB_HOST", "DATABASE_HOST",
}

// ScanDBCredentials scans Secrets in the given namespace and returns the best
// matching credential refs for the target pod.
func ScanDBCredentials(kubeconfig, namespace, podName string) (FoundCredentials, error) {
	found := FoundCredentials{Namespace: namespace, PodName: podName}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return found, fmt.Errorf("loading kubeconfig: %w", err)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return found, fmt.Errorf("creating kubernetes client: %w", err)
	}

	secrets, err := client.CoreV1().Secrets(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return found, fmt.Errorf("listing secrets in %s: %w", namespace, err)
	}

	for _, secret := range secrets.Items {
		// Skip system secrets
		if strings.HasPrefix(secret.Name, "default-token") ||
			strings.HasPrefix(secret.Name, "sh.helm") ||
			secret.Type == "kubernetes.io/service-account-token" {
			continue
		}

		keys := make(map[string]bool)
		for k := range secret.Data {
			keys[k] = true
		}

		if found.Password == nil {
			for _, k := range passwordKeys {
				if keys[k] {
					ref := &DBCredentialRef{Namespace: namespace, SecretName: secret.Name, Key: k}
					found.Password = ref
					break
				}
			}
		}
		if found.User == nil {
			for _, k := range userKeys {
				if keys[k] {
					ref := &DBCredentialRef{Namespace: namespace, SecretName: secret.Name, Key: k}
					found.User = ref
					break
				}
			}
		}
		if found.Name == nil {
			for _, k := range nameKeys {
				if keys[k] {
					ref := &DBCredentialRef{Namespace: namespace, SecretName: secret.Name, Key: k}
					found.Name = ref
					break
				}
			}
		}
		if found.Host == nil {
			for _, k := range hostKeys {
				if keys[k] {
					ref := &DBCredentialRef{Namespace: namespace, SecretName: secret.Name, Key: k}
					found.Host = ref
					break
				}
			}
		}
	}

	return found, nil
}
