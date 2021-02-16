// +build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

const secretNamespace = "openshift-config"

// TestIngressConfig test the Ingress Controller by performing the following steps:
// 1. Create an Ingress resource that defines two Spec.ComponentRoutes entries: "foo" and "bar"
// 2. Update the status or the ingress resource and ensure that a rolebinding is created.
// 3. Check that a role and RoleBinding is created for each Spec.ComponentRoutes entries in the openshift-config namespace
// 4. Remove the "bar" consumingUser from the "foo" componentRoute and make sure that the roleBinding is updated
// 5. Remove the `bar` componentRoute from the ingress spec and check that the related role and roleBinding are deleted

func TestIngressConfig(t *testing.T) {
	// 1. Create an ingress resource
	ingress := &configv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name: "ingress-test",
		},
		Spec: configv1.IngressSpec{
			ComponentRoutes: []configv1.ComponentRouteSpec{
				{
					Namespace: "default",
					Name:      "foo",
					Hostname:  "www.testing.com",
					ServingCertKeyPairSecret: configv1.SecretNameReference{
						Name: "foo",
					},
				},
				{
					Namespace: "default",
					Name:      "bar",
					Hostname:  "www.testing.com",
					ServingCertKeyPairSecret: configv1.SecretNameReference{
						Name: "bar",
					},
				},
			},
		},
	}

	if err := kclient.Create(context.TODO(), ingress); err != nil {
		t.Fatalf("failed to create ingress resource: %v", err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), ingress); err != nil {
			t.Errorf("failed to delete ingress resource: %v", err)
		}
	}()

	// 2. Update the status of the ingress resource to include consumers
	ingress.Status = configv1.IngressStatus{
		ComponentRoutes: []configv1.ComponentRouteStatus{
			{
				Namespace: "default",
				Name:      "foo",
				ConsumingUsers: []string{
					"foo",
					"bar",
				},
			},
			{
				Namespace: "default",
				Name:      "bar",
				ConsumingUsers: []string{
					"bar",
				},
			},
		},
	}
	if err := kclient.Status().Update(context.TODO(), ingress); err != nil {
		t.Fatalf("failed to get ingress resource: %v", err)
	}

	// 3. Check that a role is created for each Spec.ComponentRoutes entries in the openshift-config namespace
	for _, componentRoute := range ingress.Spec.ComponentRoutes {
		if err := pollForValidComponentRouteRole(t, componentRoute.Name, componentRoute.ServingCertKeyPairSecret.Name); err != nil {
			t.Errorf("bad role: %v", err)
		}
	}

	for _, componentRoute := range ingress.Status.ComponentRoutes {
		if err := pollForValidComponentRouteRoleBinding(t, componentRoute.Name, componentRoute.ConsumingUsers); err != nil {
			t.Errorf("bad roleBinding: %v", err)
		}
	}

	// 4. Remove the "bar" consumingUser from the "foo" componentRoute and check that the roleBinding is updated
	ingress.Status = configv1.IngressStatus{
		ComponentRoutes: []configv1.ComponentRouteStatus{
			{
				Namespace: "default",
				Name:      "foo",
				ConsumingUsers: []string{
					"foo",
				},
			},
			{
				Namespace: "default",
				Name:      "bar",
				ConsumingUsers: []string{
					"bar",
				},
			},
		},
	}

	if err := kclient.Status().Update(context.TODO(), ingress); err != nil {
		t.Fatalf("failed to get ingress resource: %v", err)
	}

	for _, componentRoute := range ingress.Status.ComponentRoutes {
		if err := pollForValidComponentRouteRoleBinding(t, componentRoute.Name, componentRoute.ConsumingUsers); err != nil {
			t.Errorf("bad roleBinding: %v", err)
		}
	}

	// 5. Remove the `bar` componentRoute from the ingress spec and check that the related role and roleBinding are deleted
	ingress.Spec = configv1.IngressSpec{
		ComponentRoutes: []configv1.ComponentRouteSpec{
			{
				Namespace: "default",
				Name:      "foo",
				Hostname:  "www.testing.com",
				ServingCertKeyPairSecret: configv1.SecretNameReference{
					Name: "foo",
				},
			},
		},
	}

	if err := wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := kclient.Update(context.TODO(), ingress); err != nil {
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Errorf("Failed to update ingress resource: %v", err)
	}

	// Make sure that the bar role and roleBinding are deleted
	role := &rbacv1.Role{}
	if err := wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := kclient.Get(context.TODO(), types.NamespacedName{"openshift-config", "bar"}, role); err == nil || !errors.IsNotFound(err) {
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Errorf("role not deleted: %v", err)
	}

	roleBinding := &rbacv1.RoleBinding{}
	if err := wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := kclient.Get(context.TODO(), types.NamespacedName{"openshift-config", "bar"}, roleBinding); err == nil || !errors.IsNotFound(err) {
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Errorf("role not deleted: %v", err)
	}
}

func pollForValidComponentRouteRole(t *testing.T, componentRouteName string, componentRouteSecretName string) error {
	role := &rbacv1.Role{}
	err := wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := kclient.Get(context.TODO(), types.NamespacedName{"openshift-config", componentRouteName}, role); err != nil {
			return false, nil
		}

		if len(role.Rules) != 1 ||
			len(role.Rules[0].Verbs) != 2 && role.Rules[0].Verbs[0] != "get" && role.Rules[0].Verbs[1] != "list" ||
			len(role.Rules[0].APIGroups) != 1 && role.Rules[0].APIGroups[0] != "" ||
			len(role.Rules[0].Resources) != 1 && role.Rules[0].Resources[0] != "secrets" ||
			len(role.Rules[0].ResourceNames) != 1 && role.Rules[0].ResourceNames[0] != componentRouteSecretName {
			return false, fmt.Errorf("Invalid Role generated")
		}

		return true, nil
	})
	return err
}

func pollForValidComponentRouteRoleBinding(t *testing.T, componentRouteName string, serviceAccounts []string) error {
	roleBinding := &rbacv1.RoleBinding{}
	err := wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := kclient.Get(context.TODO(), types.NamespacedName{"openshift-config", componentRouteName}, roleBinding); err != nil {
			t.Logf("failed to get Role %s: %v", roleBinding.GetName(), err)
			return false, nil
		}
		if roleBinding.RoleRef.APIGroup != "rbac.authorization.k8s.io" ||
			roleBinding.RoleRef.Kind != "Role" ||
			roleBinding.RoleRef.Name != componentRouteName {
			return false, nil
		}

		if len(roleBinding.Subjects) != len(serviceAccounts) {
			return false, nil
		}
		for _, expectedSubject := range serviceAccounts {
			found := false
			for _, subject := range roleBinding.Subjects {
				if subject.Name == expectedSubject {
					found = true
					break
				}
			}
			if !found {
				return false, nil
			}
		}
		return true, nil
	})

	return err
}

func TestIngressConfigGarbageCollection(t *testing.T) {
	// 1. Create an ingress resource
	ingress := &configv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name: "ingress-test",
		},
		Spec: configv1.IngressSpec{
			ComponentRoutes: []configv1.ComponentRouteSpec{
				{
					Namespace: "default",
					Name:      "foo",
					Hostname:  "www.testing.com",
					ServingCertKeyPairSecret: configv1.SecretNameReference{
						Name: "foo",
					},
				},
			},
		},
	}

	if err := kclient.Create(context.TODO(), ingress); err != nil {
		t.Fatalf("failed to create ingress resource: %v", err)
	}

	// 2. Update the status of the ingress resource to include consumers
	ingress.Status = configv1.IngressStatus{
		ComponentRoutes: []configv1.ComponentRouteStatus{
			{
				Namespace: "default",
				Name:      "foo",
				ConsumingUsers: []string{
					"foo",
				},
			},
		},
	}
	if err := kclient.Status().Update(context.TODO(), ingress); err != nil {
		t.Fatalf("failed to get ingress resource: %v", err)
	}

	// 3. Check that the expected role and rolebinding exist
	role := &rbacv1.Role{}
	if err := wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := kclient.Get(context.TODO(), types.NamespacedName{"openshift-config", "foo"}, role); err != nil {
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Errorf("role not created: %v", err)
	}

	roleBinding := &rbacv1.RoleBinding{}
	if err := wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := kclient.Get(context.TODO(), types.NamespacedName{"openshift-config", "foo"}, roleBinding); err != nil {
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Errorf("roleBidning not created: %v", err)
	}

	// 4. Delete the ingress resource
	if err := kclient.Delete(context.TODO(), ingress); err != nil {
		t.Fatalf("failed to create ingress resource: %v", err)
	}

	// 5. Make sure that the foo role and roleBinding are deleted
	if err := wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := kclient.Get(context.TODO(), types.NamespacedName{"openshift-config", "foo"}, role); err == nil || !errors.IsNotFound(err) {
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Errorf("role not deleted: %v", err)
	}

	if err := wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := kclient.Get(context.TODO(), types.NamespacedName{"openshift-config", "foo"}, roleBinding); err == nil || !errors.IsNotFound(err) {
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Errorf("role not deleted: %v", err)
	}
}
