// +build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	util "github.com/openshift/cluster-ingress-operator/pkg/util"
)

const (
	secretNamespace            = "openshift-config"
	componentRouteHashLabelKey = "ingress.operator.openshift.io/componentroutehash"
)

// TestIngressConfig tests that the Ingress Controller is correctly creating and deleting
// roles and rolebindings based on the componentRoutes defined in the ingress resource.
func TestIngressConfig(t *testing.T) {
	// Create an ingress resource
	ingress := &configv1.Ingress{}
	if err := kclient.Get(context.TODO(), types.NamespacedName{Namespace: "", Name: "cluster"}, ingress); err != nil {
		t.Fatalf("failed to create ingress resource: %v", err)
	}

	defer func() {
		if err := kclient.Get(context.TODO(), types.NamespacedName{Namespace: "", Name: "cluster"}, ingress); err != nil {
			t.Fatalf("failed to create ingress resource: %v", err)
		}
		ingress.Spec.ComponentRoutes = nil
		if err := kclient.Update(context.TODO(), ingress); err != nil {
			t.Errorf("failed to restore cluster ingress resource to original state: %v", err)
		}
		ingress.Status.ComponentRoutes = nil
		if err := kclient.Status().Update(context.TODO(), ingress); err != nil {
			t.Errorf("failed to restore cluster ingress resource to original state: %v", err)
		}
	}()

	ingress.Spec.ComponentRoutes = []configv1.ComponentRouteSpec{
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
	}

	if err := kclient.Update(context.TODO(), ingress); err != nil {
		t.Fatalf("failed to get ingress resource: %v", err)
	}

	// Update the status of the ingress resource to include consumers
	ingress.Status = configv1.IngressStatus{
		ComponentRoutes: []configv1.ComponentRouteStatus{
			{
				Namespace: "default",
				Name:      "foo",
				ConsumingUsers: []string{
					"foo",
					"bar",
				},
				DefaultHostname:  "foo.com",
				CurrentHostnames: []string{"foo.com"},
			},
			{
				Namespace: "default",
				Name:      "bar",
				ConsumingUsers: []string{
					"bar",
				},
				DefaultHostname:  "bar.com",
				CurrentHostnames: []string{"bar.com"},
			},
		},
	}
	if err := kclient.Status().Update(context.TODO(), ingress); err != nil {
		t.Fatalf("failed to get ingress resource: %v", err)
	}

	// Check that a role and roleBinding are created for each Spec.ComponentRoutes entry in the openshift-config namespace
	for _, componentRoute := range ingress.Spec.ComponentRoutes {
		if err := pollForValidComponentRouteRole(t, componentRoute); err != nil {
			t.Errorf("bad role: %v", err)
		}
	}

	for _, componentRoute := range ingress.Status.ComponentRoutes {
		if err := pollForValidComponentRouteRoleBinding(t, componentRoute); err != nil {
			t.Errorf("bad roleBinding: %v", err)
		}
	}

	// Remove the "bar" consumingUser from the "foo" componentRoute and check that the roleBinding is updated
	ingress.Status = configv1.IngressStatus{
		ComponentRoutes: []configv1.ComponentRouteStatus{
			{
				Namespace: "default",
				Name:      "foo",
				ConsumingUsers: []string{
					"foo",
				},
				DefaultHostname:  "foo.com",
				CurrentHostnames: []string{"foo.com"},
			},
			{
				Namespace: "default",
				Name:      "bar",
				ConsumingUsers: []string{
					"bar",
				},
				DefaultHostname:  "bar.com",
				CurrentHostnames: []string{"bar.com"},
			},
		},
	}

	if err := kclient.Status().Update(context.TODO(), ingress); err != nil {
		t.Fatalf("failed to get ingress resource: %v", err)
	}

	for _, componentRoute := range ingress.Status.ComponentRoutes {
		if err := pollForValidComponentRouteRoleBinding(t, componentRoute); err != nil {
			t.Errorf("bad roleBinding: %v", err)
		}
	}

	// Remove the `bar` componentRoute from the ingress spec and check that the related role and roleBinding are deleted
	ingress.Spec.ComponentRoutes = []configv1.ComponentRouteSpec{
		{
			Namespace: "default",
			Name:      "foo",
			Hostname:  "www.testing.com",
			ServingCertKeyPairSecret: configv1.SecretNameReference{
				Name: "foo",
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
	roleList := &rbacv1.RoleList{}
	listOptions := []client.ListOption{
		client.MatchingLabels{
			componentRouteHashLabelKey: util.Hash("default/bar"),
		},
	}
	if err := wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := kclient.List(context.TODO(), roleList, listOptions...); err != nil {
			return false, err
		}
		if len(roleList.Items) != 0 {
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Errorf("role not deleted: %v", err)
	}

	roleBindingList := &rbacv1.RoleBindingList{}
	if err := wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := kclient.List(context.TODO(), roleBindingList, listOptions...); err != nil {
			return false, err
		}
		if len(roleBindingList.Items) != 0 {
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Errorf("role not deleted: %v", err)
	}
}

func pollForValidComponentRouteRole(t *testing.T, componentRoute configv1.ComponentRouteSpec) error {
	listOptions := []client.ListOption{
		client.MatchingLabels{
			componentRouteHashLabelKey: util.Hash(fmt.Sprintf("%s/%s", componentRoute.Namespace, componentRoute.Name)),
		},
	}

	roleList := &rbacv1.RoleList{}
	err := wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := kclient.List(context.TODO(), roleList, listOptions...); err != nil {
			return false, nil
		}

		if len(roleList.Items) != 1 {
			return false, nil
		}

		role := roleList.Items[0]
		if len(role.Rules) != 1 ||
			len(role.Rules[0].Verbs) != 3 && role.Rules[0].Verbs[0] != "get" && role.Rules[0].Verbs[1] != "list" && role.Rules[0].Verbs[2] != "watch" ||
			len(role.Rules[0].APIGroups) != 1 && role.Rules[0].APIGroups[0] != "" ||
			len(role.Rules[0].Resources) != 1 && role.Rules[0].Resources[0] != "secrets" ||
			len(role.Rules[0].ResourceNames) != 1 && role.Rules[0].ResourceNames[0] != componentRoute.ServingCertKeyPairSecret.Name {

			return false, fmt.Errorf("Invalid Role generated")
		}

		return true, nil
	})
	return err
}

func pollForValidComponentRouteRoleBinding(t *testing.T, componentRoute configv1.ComponentRouteStatus) error {
	listOptions := []client.ListOption{
		client.MatchingLabels{
			componentRouteHashLabelKey: util.Hash(fmt.Sprintf("%s/%s", componentRoute.Namespace, componentRoute.Name)),
		},
	}

	roleBindingList := &rbacv1.RoleBindingList{}
	err := wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := kclient.List(context.TODO(), roleBindingList, listOptions...); err != nil {
			return false, nil
		}

		if len(roleBindingList.Items) != 1 {
			return false, nil
		}

		roleBinding := roleBindingList.Items[0]
		if roleBinding.RoleRef.APIGroup != "rbac.authorization.k8s.io" ||
			roleBinding.RoleRef.Kind != "Role" ||
			roleBinding.RoleRef.Name != roleBinding.GetName() {
			return false, nil
		}

		if len(roleBinding.Subjects) != len(componentRoute.ConsumingUsers) {
			return false, nil
		}

		for _, expectedSubject := range componentRoute.ConsumingUsers {
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
