package ingressroutes

import (
	"context"
	"fmt"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	util "github.com/openshift/cluster-ingress-operator/pkg/util"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	configv1 "github.com/openshift/api/config/v1"

	"k8s.io/apimachinery/pkg/api/errors"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	controllerName             = "operator_route_rbac_controller"
	componentRouteHashLabelKey = "ingress.operator.openshift.io/componentroutehash"
)

var (
	log = logf.Logger.WithName(controllerName)
)

// New creates the ingress routes controller from configuration. This is the controller
// that handles all the logic for generating roles and rolebindings for operators that
// include routes with configurable hostnames and serving certificate.
func New(mgr manager.Manager, config Config) (controller.Controller, error) {
	reconciler := &reconciler{
		config: config,
		client: mgr.GetClient(),
		cache:  mgr.GetCache(),
	}
	c, err := controller.New(controllerName, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}

	// Trigger reconcile requests for the cluster ingress resource.
	clusterNamePredicate := predicate.NewPredicateFuncs(func(meta metav1.Object, object runtime.Object) bool {
		ingressNamespacedName := operatorcontroller.IngressClusterConfigName()
		return meta.GetName() == ingressNamespacedName.Name && meta.GetNamespace() == ingressNamespacedName.Namespace
	})

	if err := c.Watch(&source.Kind{Type: &configv1.Ingress{}}, &handler.EnqueueRequestForObject{}, clusterNamePredicate); err != nil {
		return nil, err
	}

	// Trigger reconcile requests for the roles and roleBindings with the componentRoute label.
	defaultPredicate := predicate.NewPredicateFuncs(func(meta metav1.Object, object runtime.Object) bool {
		labels := meta.GetLabels()
		_, ok := labels[componentRouteHashLabelKey]
		return ok
	})

	roleInformer, err := mgr.GetCache().GetInformer(context.TODO(), &rbacv1.Role{})
	if err := c.Watch(&source.Informer{Informer: roleInformer}, &handler.EnqueueRequestsFromMapFunc{ToRequests: handler.ToRequestsFunc(reconciler.resourceToClusterIngressConfig)}, defaultPredicate); err != nil {
		return nil, err
	}

	roleBindingInformer, err := mgr.GetCache().GetInformer(context.TODO(), &rbacv1.RoleBinding{})
	if err := c.Watch(&source.Informer{Informer: roleBindingInformer}, &handler.EnqueueRequestsFromMapFunc{ToRequests: handler.ToRequestsFunc(reconciler.resourceToClusterIngressConfig)}, defaultPredicate); err != nil {
		return nil, err
	}
	return c, nil
}

// resourceToClusterIngressConfig is used to only trigger reconciles on the cluster ingress config
func (r *reconciler) resourceToClusterIngressConfig(o handler.MapObject) []reconcile.Request {
	return []reconcile.Request{
		{
			operatorcontroller.IngressClusterConfigName(),
		},
	}
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
	cache  cache.Cache
}

// Reconcile expects request to refer to a ingress in the operator namespace,
// and will do all the work to ensure the ingress is in the desired state.
func (r *reconciler) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	// Only proceed if we can get the ingress resource.
	ingress := &configv1.Ingress{}
	if err := r.cache.Get(context.TODO(), request.NamespacedName, ingress); err != nil {
		if errors.IsNotFound(err) {
			log.Info("ingress cr not found; reconciliation will be skipped", "request", request)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to get ingress %q: %v", request, err)
	}

	// Get the list of componentRoutes defined in both the spec and status of the ingress resource
	componentRoutes := r.intersectingComponentRoutes(ingress.Spec.ComponentRoutes, ingress.Status.ComponentRoutes)

	// Ensure role and roleBindings exist for each valid componentRoute.
	for _, componentRoute := range componentRoutes {
		roleName, err := r.ensureServiceCertKeyPairSecretRole(ingress, componentRoute)
		if err != nil {
			return reconcile.Result{Requeue: true}, fmt.Errorf("failed to create role: %v", err)
		}

		if err := r.ensureServiceCertKeyPairSecretRoleBinding(ingress, componentRoute, roleName); err != nil {
			return reconcile.Result{Requeue: true}, fmt.Errorf("failed to create rolebinding: %v", err)
		}
	}

	// Delete any roles or roleBindings that were generated for componentRoutes that are no longer defined.
	if err := r.cleanupOrphanedResources(componentRoutes); err != nil {
		return reconcile.Result{Requeue: true}, fmt.Errorf("failed to delete role: %v", err)
	}
	return reconcile.Result{}, nil
}

func (r *reconciler) intersectingComponentRoutes(componentRouteSpecs []configv1.ComponentRouteSpec, componentRouteStatuses []configv1.ComponentRouteStatus) []aggregatedComponentRoute {
	componentRouteHashToComponentRouteStatus := map[string]configv1.ComponentRouteStatus{}
	for _, componentRoute := range componentRouteStatuses {
		componentRouteHash := util.Hash(namespacedName(componentRoute.Namespace, componentRoute.Name))
		componentRouteHashToComponentRouteStatus[componentRouteHash] = componentRoute
	}

	componentRoutes := []aggregatedComponentRoute{}
	for _, componentRouteSpec := range componentRouteSpecs {
		hash := util.Hash(namespacedName(componentRouteSpec.Namespace, componentRouteSpec.Name))
		if componentRouteStatus, ok := componentRouteHashToComponentRouteStatus[hash]; ok {
			componentRoutes = append(componentRoutes, newAggregatedComponentRoute(componentRouteSpec, componentRouteStatus))
		}
	}
	return componentRoutes
}

// aggregatedComponeRoute contains all the information from the ComponentRouteSpec
// and ComponentRouteStatus to generate the required Role and RoleBinding.
type aggregatedComponentRoute struct {
	Name                   string
	Hash                   string
	ServingCertificateName string
	ConsumingUsers         []string
}

func newAggregatedComponentRoute(spec configv1.ComponentRouteSpec, status configv1.ComponentRouteStatus) aggregatedComponentRoute {
	return aggregatedComponentRoute{
		Name:                   spec.Name,
		Hash:                   util.Hash(namespacedName(spec.Namespace, spec.Name)),
		ServingCertificateName: spec.ServingCertKeyPairSecret.Name,
		ConsumingUsers:         status.ConsumingUsers,
	}
}

func namespacedName(namespace, name string) string {
	return fmt.Sprintf("%s/%s", namespace, name)
}

func componentRouteResources(componentRoute aggregatedComponentRoute) []client.ListOption {
	return []client.ListOption{
		client.MatchingLabels{
			componentRouteHashLabelKey: componentRoute.Hash,
		},
		client.InNamespace(operatorcontroller.OpenShiftConfigNamespace),
	}
}

func allComponentRouteResources() []client.ListOption {
	return []client.ListOption{
		client.HasLabels{componentRouteHashLabelKey},
		client.InNamespace(operatorcontroller.OpenShiftConfigNamespace),
	}
}

func (r *reconciler) cleanupOrphanedResources(componentRoutes []aggregatedComponentRoute) error {
	existingHashes := map[string]struct{}{}
	for _, cr := range componentRoutes {
		existingHashes[cr.Hash] = struct{}{}
	}

	roleList := &rbacv1.RoleList{}
	r.cache.List(context.TODO(), roleList, allComponentRouteResources()...)
	for _, item := range roleList.Items {
		expectedHash, ok := item.GetLabels()[componentRouteHashLabelKey]
		if !ok {
			return fmt.Errorf("Unable to find expected componentRoute hash label")
		}

		if _, ok := existingHashes[expectedHash]; !ok {
			log.Info("deleting role", "name", item.GetName(), "namespace", item.GetNamespace())
			if err := r.client.Delete(context.TODO(), &item); err != nil && !errors.IsNotFound(err) {
				return err
			}
		}
	}

	roleBindingList := &rbacv1.RoleBindingList{}
	r.cache.List(context.TODO(), roleBindingList, allComponentRouteResources()...)
	for _, item := range roleBindingList.Items {
		expectedHash, ok := item.GetLabels()[componentRouteHashLabelKey]
		if !ok {
			return fmt.Errorf("Unable to find expected componentRoute hash label")
		}

		if _, ok := existingHashes[expectedHash]; !ok {
			log.Info("deleting roleBinding", "name", item.GetName(), "namespace", item.GetNamespace())
			if err := r.client.Delete(context.TODO(), &item); err != nil && !errors.IsNotFound(err) {
				return err
			}
		}
	}

	return nil
}

func (r *reconciler) ensureServiceCertKeyPairSecretRole(owner metav1.Object, componentRoute aggregatedComponentRoute) (string, error) {
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: componentRoute.Name + "-",
			Namespace:    r.config.SecretNamespace,
			Labels: map[string]string{
				componentRouteHashLabelKey: componentRoute.Hash,
			},
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:         []string{"get", "list", "watch"},
				APIGroups:     []string{""},
				Resources:     []string{"secrets"},
				ResourceNames: []string{componentRoute.ServingCertificateName},
			},
		},
	}

	roleList := &rbacv1.RoleList{}
	if err := r.cache.List(context.TODO(), roleList, componentRouteResources(componentRoute)...); err != nil {
		return "", err
	}

	if len(roleList.Items) == 0 {
		if err := r.client.Create(context.TODO(), role); err != nil {
			return "", err
		}
		return role.GetName(), nil
	}

	for i, curRole := range roleList.Items {
		if i == 0 {
			continue
		}
		if err := r.client.Delete(context.TODO(), &curRole); err != nil && !errors.IsNotFound(err) {
			return "", err
		}
	}
	existingRole := roleList.Items[0]
	existingRole.Rules = role.Rules
	if err := r.client.Update(context.TODO(), &existingRole); err != nil {
		return "", err
	}
	return existingRole.GetName(), nil
}

func (r *reconciler) ensureServiceCertKeyPairSecretRoleBinding(owner metav1.Object, componentRoute aggregatedComponentRoute, roleName string) error {
	subjects := []rbacv1.Subject{}
	for _, serviceAccountName := range componentRoute.ConsumingUsers {
		subjects = append(subjects, rbacv1.Subject{
			Kind:     rbacv1.ServiceAccountKind,
			Name:     serviceAccountName,
			APIGroup: "",
		})
	}

	roleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleName,
			Namespace: r.config.SecretNamespace,
			Labels: map[string]string{
				componentRouteHashLabelKey: componentRoute.Hash,
			},
		},
		Subjects: subjects,
		RoleRef: rbacv1.RoleRef{
			Kind:     "Role",
			Name:     roleName,
			APIGroup: "rbac.authorization.k8s.io",
		},
	}

	roleBindingList := &rbacv1.RoleBindingList{}
	if err := r.cache.List(context.TODO(), roleBindingList, componentRouteResources(componentRoute)...); err != nil {
		return err
	}
	if len(roleBindingList.Items) == 0 {
		return r.client.Create(context.TODO(), roleBinding)
	}

	for i, curRole := range roleBindingList.Items {
		if i == 0 {
			continue
		}
		if err := r.client.Delete(context.TODO(), &curRole); err != nil && !errors.IsNotFound(err) {

			return err
		}
	}
	existingRoleBinding := roleBindingList.Items[0]
	existingRoleBinding.Subjects = roleBinding.Subjects
	existingRoleBinding.RoleRef = roleBinding.RoleRef
	return r.client.Update(context.TODO(), &existingRoleBinding)
}
