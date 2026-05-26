package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	Namespace     = "dbpilot"
	AgeSecretName = "dbpilot-age-key"
	AgeSecretKey  = "private_key"
	S3SecretName  = "s3-credentials"
)

// StoreAgePrivateKey stores the age private key in a K8s Secret.
func StoreAgePrivateKey(kubeconfig, privateKey string) error {
	return storeSecret(kubeconfig, AgeSecretName, map[string]string{
		AgeSecretKey: privateKey,
	})
}

// StoreS3Credentials stores S3 access/secret keys in a K8s Secret.
func StoreS3Credentials(kubeconfig, accessKey, secretKey string) error {
	return storeSecret(kubeconfig, S3SecretName, map[string]string{
		"access_key": accessKey,
		"secret_key": secretKey,
	})
}

// CopySecretToNamespace copies a secret from the dbpilot namespace to another namespace.
func CopySecretToNamespace(kubeconfig, secretName, targetNamespace string) error {
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return fmt.Errorf("loading kubeconfig: %w", err)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("creating kubernetes client: %w", err)
	}

	src, err := client.CoreV1().Secrets(Namespace).Get(context.Background(), secretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("reading secret %q from %s: %w", secretName, Namespace, err)
	}

	dst := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: targetNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "dbpilot",
				"dbpilot/copied-from":          Namespace,
			},
		},
		Type: src.Type,
		Data: src.Data,
	}

	_, err = client.CoreV1().Secrets(targetNamespace).Create(context.Background(), dst, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		_, err = client.CoreV1().Secrets(targetNamespace).Update(context.Background(), dst, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("copying secret to %s: %w", targetNamespace, err)
	}
	return nil
}

func storeSecret(kubeconfig, name string, data map[string]string) error {
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return fmt.Errorf("loading kubeconfig: %w", err)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("creating kubernetes client: %w", err)
	}

	if err := ensureNamespace(client); err != nil {
		return err
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "dbpilot",
			},
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: data,
	}

	_, err = client.CoreV1().Secrets(Namespace).Create(context.Background(), secret, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		_, err = client.CoreV1().Secrets(Namespace).Update(context.Background(), secret, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("storing secret %q: %w", name, err)
	}
	return nil
}

func ensureNamespace(client kubernetes.Interface) error {
	_, err := client.CoreV1().Namespaces().Get(context.Background(), Namespace, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("checking namespace: %w", err)
	}
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "dbpilot",
			},
		},
	}
	_, err = client.CoreV1().Namespaces().Create(context.Background(), ns, metav1.CreateOptions{})
	return err
}
