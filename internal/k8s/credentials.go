package k8s

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
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
}

// ScanDBCredentials scans secrets in the namespace looking for Postgres credentials
// associated with the given pod.
func ScanDBCredentials(kubeconfig, namespace, podName string) (DBCredentials, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return DBCredentials{}, err
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return DBCredentials{}, err
	}

	// Get the pod to find its env vars referencing secrets
	pod, err := client.CoreV1().Pods(namespace).Get(context.Background(), podName, metav1.GetOptions{})
	if err != nil {
		return DBCredentials{}, err
	}

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

const S3SecretName = "s3-credentials"

// StoreS3Credentials creates or updates the s3-credentials Secret in the dbpilot namespace.
func StoreS3Credentials(kubeconfig, accessKey, secretKey string) error {
	return storeS3CredentialsInNamespace(kubeconfig, "dbpilot", accessKey, secretKey)
}

func storeS3CredentialsInNamespace(kubeconfig, namespace, accessKey, secretKey string) error {
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
			Name:      S3SecretName,
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
