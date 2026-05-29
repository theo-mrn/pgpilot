package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func stripPrefix(s, prefix string) string {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return s
}

func indexOf(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// parseRef parses a k8s-secret://namespace/name#key reference.
// Returns namespace, name, key.
func parseRef(ref string) (namespace, name, key string) {
	ref = stripPrefix(ref, "k8s-secret://")
	if i := indexOf(ref, '#'); i >= 0 {
		key = ref[i+1:]
		ref = ref[:i]
	}
	if i := indexOf(ref, '/'); i >= 0 {
		namespace = ref[:i]
		name = ref[i+1:]
	} else {
		name = ref
	}
	return
}

// secretKeySelector returns a *corev1.SecretKeySelector for a k8s-secret:// ref.
// The namespace is stripped — K8s selectors are namespace-local.
func secretKeySelector(ref string) *corev1.SecretKeySelector {
	_, name, key := parseRef(ref)
	return &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: name},
		Key:                  key,
	}
}

// readSecret resolves a k8s-secret:// ref to its plaintext value.
func readSecret(client kubernetes.Interface, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	ns, name, key := parseRef(ref)
	return readSecretByKey(client, ns, name, key)
}

// readSecretByKey reads a specific key from a secret directly.
func readSecretByKey(client kubernetes.Interface, namespace, secretName, key string) (string, error) {
	secret, err := client.CoreV1().Secrets(namespace).Get(context.Background(), secretName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("reading secret %s/%s: %w", namespace, secretName, err)
	}
	val, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %s/%s", key, namespace, secretName)
	}
	return string(val), nil
}

// upsertSecret creates or updates a Secret.
func upsertSecret(client kubernetes.Interface, secret *corev1.Secret) error {
	_, err := client.CoreV1().Secrets(secret.Namespace).Create(context.Background(), secret, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		_, err = client.CoreV1().Secrets(secret.Namespace).Update(context.Background(), secret, metav1.UpdateOptions{})
	}
	return err
}

// managedSecret returns a Secret with standard dbpilot labels.
func managedSecret(namespace, name string, data map[string]string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{ManagedByLabel: ManagedByValue},
		},
		StringData: data,
	}
}
