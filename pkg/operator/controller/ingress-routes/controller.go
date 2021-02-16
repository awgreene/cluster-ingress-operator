package ingress

import (
	"context"
	"fmt"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	configv1 "github.com/openshift/api/config/v1"

	"k8s.io/apimachinery/pkg/api/errors"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	controllerName = "ingress_routes_controller"
)

var (
	log = logf.Logger.WithName(controllerName)
)

// New creates the ingress routes controller from configuration. This is the controller
// that handles all the logic for generating roles and rolebindings for operators that
// include routes with configurable hostnames and service certificate.
func New(mgr manager.Manager, config Config) (controller.Controller, error) {
	reconciler := &reconciler{
		config: config,
		client: mgr.GetClient(),
	}
	c, err := controller.New(controllerName, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}
	if err := c.Watch(&source.Kind{Type: &configv1.Ingress{}}, &handler.EnqueueRequestForObject{}); err != nil {
		return nil, err
	}
	// TODO: The operator only watches for events in the openshift-ingress-operator namespace,
	// changes to generated role and rolebindings in the openshift-config namespace are ignored.
	// Find a way to trigger events outside of the openshift-ingress-operator namespace.
	if err := c.Watch(&source.Kind{Type: &rbacv1.Role{}}, &handler.EnqueueRequestForOwner{OwnerType: &configv1.Ingress{}}); err != nil {
		return nil, err
	}
	if err := c.Watch(&source.Kind{Type: &rbacv1.RoleBinding{}}, &handler.EnqueueRequestForOwner{OwnerType: &configv1.Ingress{}}); err != nil {
		return nil, err
	}
	return c, nil
}

// Config holds all the things necessary for the controller to run.
type Config struct {
	SecretNamespace string
}

// reconciler handles the actual ingress reconciliation logic in response to
// events.
type reconciler struct {
	config Config
	client client.Client
}

type componentRouteTuple struct {
	Namespace string
	Name      string
}

// Reconcile expects request to refer to a ingress in the operator namespace,
// and will do all the work to ensure the ingress is in the desired state.
func (r *reconciler) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	// Only proceed if we can get the ingress resource.
	ingress := &configv1.Ingress{}
	if err := r.client.Get(context.TODO(), request.NamespacedName, ingress); err != nil {
		if errors.IsNotFound(err) {
			log.Info("ingress cr not found; reconciliation will be skipped", "request", request)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to get ingress %q: %v", request, err)
	}

	// Generate namespaceName mapping
	componentRouteTupleToSecretName := map[componentRouteTuple]string{}
	for _, componentRoute := range ingress.Spec.ComponentRoutes {
		tuple := componentRouteTuple{Namespace: componentRoute.Namespace, Name: componentRoute.Name}
		componentRouteTupleToSecretName[tuple] = componentRoute.ServingCertKeyPairSecret.Name
	}

	// For each Status.ComponentRoute resource, if a Spec.ComponentRoute with a matching
	// namespace.name tuple exists create the role and roleBinding for each consumer users.
	// If no matching Spec.ComponentRoute exists, make sure any RBAC that was generated is removed.
	for _, componentRoute := range ingress.Status.ComponentRoutes {
		tuple := componentRouteTuple{Namespace: componentRoute.Namespace, Name: componentRoute.Name}
		secretName, ok := componentRouteTupleToSecretName[tuple]

		// Delete the generated role and roleBinding if they exists for a component not defined in the spec.
		if !ok {
			if err := r.deleteServiceKeyPairSecretRole(componentRoute.Name); err != nil {
				return reconcile.Result{Requeue: true}, err
			}

			if err := r.deleteServiceKeyPairSecretRoleBinding(componentRoute.Name); err != nil {
				return reconcile.Result{Requeue: true}, err
			}

			continue
		}

		if err := r.ensureServiceCertKeyPairSecretRole(ingress, componentRoute.Name, secretName); err != nil {
			return reconcile.Result{Requeue: true}, err
		}

		if err := r.ensureServiceCertKeyPairSecretRoleBinding(ingress, componentRoute.Name, componentRoute.ConsumingUsers); err != nil {
			return reconcile.Result{Requeue: true}, err
		}
	}
	return reconcile.Result{}, nil
}

func (r *reconciler) ensureServiceCertKeyPairSecretRole(owner metav1.Object, crName, secretName string) error {
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			// TODO: Generate Name and add labels to avoid componentRoute.Name collisions.
			Name:      crName,
			Namespace: r.config.SecretNamespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:         []string{"get", "list", "watch"},
				APIGroups:     []string{""},
				Resources:     []string{"secrets"},
				ResourceNames: []string{secretName},
			},
		},
	}

	// Attempt to create or update the roleBinding
	if err := r.client.Create(context.TODO(), role); err != nil {
		if !errors.IsAlreadyExists(err) {
			return err
		}

		existingRole := &rbacv1.Role{}
		if err := r.client.Get(context.TODO(), client.ObjectKey{Name: role.GetName(), Namespace: role.GetNamespace()}, existingRole); err != nil {
			return err
		}

		// TODO: Remove comment below once decision is made.
		// Hard update of spec - could apply hash annotation of desired spec
		// to role to avoid race conditions with other controllers.
		existingRole.Rules = role.Rules
		return r.client.Update(context.TODO(), existingRole)
	}

	return nil
}

func (r *reconciler) ensureServiceCertKeyPairSecretRoleBinding(owner metav1.Object, roleName string, users []string) error {
	subjects := []rbacv1.Subject{}
	for _, user := range users {
		subjects = append(subjects, rbacv1.Subject{
			Kind:     rbacv1.ServiceAccountKind, // TODO: Should we support non service accounts?
			Name:     user,
			APIGroup: "",
		})
	}

	roleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleName,
			Namespace: r.config.SecretNamespace,
		},
		Subjects: subjects,
		RoleRef: rbacv1.RoleRef{
			Kind:     "Role",
			Name:     roleName,
			APIGroup: "rbac.authorization.k8s.io",
		},
	}

	// Attempt to create or update the roleBinding
	if err := r.client.Create(context.TODO(), roleBinding); err != nil {
		if !errors.IsAlreadyExists(err) {
			return err
		}

		existingRoleBinding := &rbacv1.RoleBinding{}
		if err := r.client.Get(context.TODO(), client.ObjectKey{Name: roleBinding.GetName(), Namespace: roleBinding.GetNamespace()}, existingRoleBinding); err != nil {
			return err
		}

		// TODO: Remove comment below once decision is made.
		// Hard update of spec - could apply hash annotation of desired spec
		// to role to avoid race conditions with other controllers.
		existingRoleBinding.Subjects = roleBinding.Subjects
		existingRoleBinding.RoleRef = roleBinding.RoleRef
		return r.client.Update(context.TODO(), existingRoleBinding)
	}

	return nil
}

func (r *reconciler) deleteServiceKeyPairSecretRole(name string) error {
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: r.config.SecretNamespace,
		},
	}
	if err := r.client.Delete(context.TODO(), role); err != nil && !errors.IsNotFound(err) {
		return err
	}

	return nil
}

func (r *reconciler) deleteServiceKeyPairSecretRoleBinding(name string) error {
	rolebinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: r.config.SecretNamespace,
		},
	}
	if err := r.client.Delete(context.TODO(), rolebinding); err != nil && !errors.IsNotFound(err) {
		return err
	}

	return nil
}
