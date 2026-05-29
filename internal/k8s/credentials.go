package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/theomorin/dbpilot/internal/config"
)

// DBCredentialRef holds a resolved secret key reference for a DB credential.
type DBCredentialRef struct {
	Namespace  string
	SecretName string
	Key        string
}

func (r *DBCredentialRef) SecretRefString() string {
	return fmt.Sprintf("k8s-secret://%s/%s#%s", r.Namespace, r.SecretName, r.Key)
}

// DBCredentials holds resolved refs for password, user, and db name.
type DBCredentials struct {
	Password *DBCredentialRef
	User     *DBCredentialRef
	Name     *DBCredentialRef
	DBName   string // plain value (used when db name is known directly, e.g. CNPG)
}

// ScanDBCredentials scans secrets in the namespace looking for Postgres credentials
// associated with the given pod. Handles both vanilla Postgres and CNPG clusters.
func ScanDBCredentials(kubeconfig, namespace, podName string) (DBCredentials, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return DBCredentials{}, err
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return DBCredentials{}, err
	}

	pod, err := client.CoreV1().Pods(namespace).Get(context.Background(), podName, metav1.GetOptions{})
	if err != nil {
		return DBCredentials{}, err
	}

	// CNPG pods have the label cnpg.io/cluster — credentials are in <cluster>-credentials secret.
	if clusterName, ok := pod.Labels["cnpg.io/cluster"]; ok {
		return scanCNPGCredentials(kubeconfig, namespace, clusterName)
	}

	// Vanilla Postgres — scan env vars referencing secrets.
	var result DBCredentials
	for _, container := range pod.Spec.Containers {
		for _, env := range container.Env {
			if env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
				continue
			}
			ref := &DBCredentialRef{
				Namespace:  namespace,
				SecretName: env.ValueFrom.SecretKeyRef.Name,
				Key:        env.ValueFrom.SecretKeyRef.Key,
			}
			upper := strings.ToUpper(env.Name)
			if result.Password == nil && (upper == "POSTGRES_PASSWORD" || upper == "DB_PASSWORD" || upper == "PGPASSWORD") {
				result.Password = ref
			}
			if result.User == nil && (upper == "POSTGRES_USER" || upper == "DB_USER" || upper == "PGUSER") {
				result.User = ref
			}
			if result.Name == nil && (upper == "POSTGRES_DB" || upper == "DB_NAME" || upper == "PGDATABASE") {
				result.Name = ref
			}
		}
	}
	return result, nil
}

// scanCNPGCredentials returns credential refs for a CNPG cluster.
// CNPG stores credentials in a secret named <cluster>-credentials with keys username/password.
// The DB name is read from the Cluster CRD spec.bootstrap.initdb.database.
func scanCNPGCredentials(kubeconfig, namespace, clusterName string) (DBCredentials, error) {
	secretName := clusterName + "-credentials"
	creds := DBCredentials{
		Password: &DBCredentialRef{
			Namespace:  namespace,
			SecretName: secretName,
			Key:        "password",
		},
		User: &DBCredentialRef{
			Namespace:  namespace,
			SecretName: secretName,
			Key:        "username",
		},
	}

	// Try to read the DB name from the CNPG Cluster CRD.
	if dbName := readCNPGDatabaseName(kubeconfig, namespace, clusterName); dbName != "" {
		creds.DBName = dbName
	}

	return creds, nil
}

// CNPGHasReplicationHBA checks if the CNPG Cluster already has a pg_hba rule for replication.
func CNPGHasReplicationHBA(kubeconfig string, job config.JobConfig) bool {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return false
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return false
	}
	clusterName := job.Credentials.DBHost
	if len(clusterName) > 3 {
		clusterName = clusterName[:len(clusterName)-3] // strip "-rw"
	}
	data, err := client.RESTClient().Get().
		AbsPath("/apis/postgresql.cnpg.io/v1").
		Namespace(job.Environment.Namespace).
		Resource("clusters").
		Name(clusterName).
		DoRaw(context.Background())
	if err != nil {
		return false
	}
	var cluster struct {
		Spec struct {
			PostgreSQL struct {
				PgHBA []string `json:"pg_hba"`
			} `json:"postgresql"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(data, &cluster); err != nil {
		return false
	}
	for _, rule := range cluster.Spec.PostgreSQL.PgHBA {
		if strings.Contains(rule, "replication") && strings.Contains(rule, "pgpilot") {
			return true
		}
	}
	return false
}

// readCNPGDatabaseName reads spec.bootstrap.initdb.database from the CNPG Cluster CRD
// using the raw REST API (no generated client needed).
func readCNPGDatabaseName(kubeconfig, namespace, clusterName string) string {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return ""
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return ""
	}
	// Use the dynamic REST client to fetch the CNPG Cluster CRD.
	data, err := client.RESTClient().Get().
		AbsPath("/apis/postgresql.cnpg.io/v1").
		Namespace(namespace).
		Resource("clusters").
		Name(clusterName).
		DoRaw(context.Background())
	if err != nil {
		return ""
	}
	// Parse just the field we need without importing generated CNPG types.
	type cnpgCluster struct {
		Spec struct {
			Bootstrap struct {
				InitDB struct {
					Database string `json:"database"`
				} `json:"initdb"`
			} `json:"bootstrap"`
		} `json:"spec"`
	}
	var cluster cnpgCluster
	if err := json.Unmarshal(data, &cluster); err != nil {
		return ""
	}
	return cluster.Spec.Bootstrap.InitDB.Database
}

const S3SecretName = "s3-credentials"

// StoreS3Credentials creates or updates the s3-credentials Secret in the dbpilot namespace.
func StoreS3Credentials(kubeconfig, accessKey, secretKey string) error {
	return storeS3CredentialsInNamespace(kubeconfig, "dbpilot", accessKey, secretKey)
}

// StoreS3CredentialsNamed creates or updates a Secret with the given name in the dbpilot namespace.
func StoreS3CredentialsNamed(kubeconfig, secretName, accessKey, secretKey string) error {
	return storeS3CredentialsNamed(kubeconfig, "dbpilot", secretName, accessKey, secretKey)
}

func storeS3CredentialsInNamespace(kubeconfig, namespace, accessKey, secretKey string) error {
	return storeS3CredentialsNamed(kubeconfig, namespace, S3SecretName, accessKey, secretKey)
}

func storeS3CredentialsNamed(kubeconfig, namespace, secretName, accessKey, secretKey string) error {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return err
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "dbpilot"},
		},
		StringData: map[string]string{
			"access_key": accessKey,
			"secret_key": secretKey,
		},
	}

	_, err = client.CoreV1().Secrets(namespace).Create(context.Background(), secret, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		_, err = client.CoreV1().Secrets(namespace).Update(context.Background(), secret, metav1.UpdateOptions{})
	}
	return err
}

// ReadSecret reads a key from a K8s secret using a k8s-secret:// ref string.
func ReadSecret(kubeconfig, ref string) (string, error) {
	// ref format: k8s-secret://namespace/name#key
	ref = stripPrefix(ref, "k8s-secret://")
	hashIdx := indexOf(ref, '#')
	key := ""
	if hashIdx >= 0 {
		key = ref[hashIdx+1:]
		ref = ref[:hashIdx]
	}
	slashIdx := indexOf(ref, '/')
	namespace := "default"
	name := ref
	if slashIdx >= 0 {
		namespace = ref[:slashIdx]
		name = ref[slashIdx+1:]
	}

	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return "", err
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return "", err
	}
	secret, err := client.CoreV1().Secrets(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("reading secret %s/%s: %w", namespace, name, err)
	}
	val, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %s/%s", key, namespace, name)
	}
	return string(val), nil
}

// StoreAgePrivateKey stores the age private key in a Secret in the dbpilot namespace.
func StoreAgePrivateKey(kubeconfig, privateKey string) error {
	return storeAgePrivateKeyInNamespace(kubeconfig, "dbpilot", privateKey)
}

func storeAgePrivateKeyInNamespace(kubeconfig, namespace, privateKey string) error {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return err
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dbpilot-age-key",
			Namespace: namespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "dbpilot"},
		},
		StringData: map[string]string{
			"private_key": privateKey,
		},
	}

	_, err = client.CoreV1().Secrets(namespace).Create(context.Background(), secret, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		_, err = client.CoreV1().Secrets(namespace).Update(context.Background(), secret, metav1.UpdateOptions{})
	}
	return err
}
