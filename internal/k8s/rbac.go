package k8s

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const BackupServiceAccount = "dbpilot-backup"

func EnsureBackupRBAC(client kubernetes.Interface, namespace string) error {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      BackupServiceAccount,
			Namespace: namespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "dbpilot"},
		},
	}
	_, err := client.CoreV1().ServiceAccounts(namespace).Create(context.Background(), sa, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dbpilot-exec",
			Namespace: namespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "dbpilot"},
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"pods/exec"},
				Verbs:     []string{"create"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"get", "list"},
			},
		},
	}
	_, err = client.RbacV1().Roles(namespace).Create(context.Background(), role, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dbpilot-exec",
			Namespace: namespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "dbpilot"},
		},
		Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: BackupServiceAccount, Namespace: namespace}},
		RoleRef:  rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "dbpilot-exec"},
	}
	_, err = client.RbacV1().RoleBindings(namespace).Create(context.Background(), rb, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	return nil
}
