package v1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Ingress holds cluster-wide information about ingress, including the default ingress domain
// used for routes. The canonical name is `cluster`.
type Ingress struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec holds user settable values for configuration
	// +kubebuilder:validation:Required
	// +required
	Spec IngressSpec `json:"spec"`
	// status holds observed values from the cluster. They may not be overridden.
	// +optional
	Status IngressStatus `json:"status"`
}

type IngressSpec struct {
	// domain is used to generate a default host name for a route when the
	// route's host name is empty. The generated host name will follow this
	// pattern: "<route-name>.<route-namespace>.<domain>".
	//
	// It is also used as the default wildcard domain suffix for ingress. The
	// default ingresscontroller domain will follow this pattern: "*.<domain>".
	//
	// Once set, changing domain is not currently supported.
	Domain string `json:"domain"`

	// appsDomain is an optional domain to use instead of the one specified
	// in the domain field when a Route is created without specifying an explicit
	// host. If appsDomain is nonempty, this value is used to generate default
	// host values for Route. Unlike domain, appsDomain may be modified after
	// installation.
	// This assumes a new ingresscontroller has been setup with a wildcard
	// certificate.
	// +optional
	AppsDomain string `json:"appsDomain,omitempty"`

	// ComponentRoutes is a list of routes that a cluster-admin wants to customize.  It is logically keyed by
	// .spec.componentRoutes[index].{namespace,name}.
	// To determine the set of possible keys, look at .status.componentRoutes where participating operators place
	// current route status keyed the same way.
	// If a ComponentRoute is created with a namespace,name tuple that does not match status, that piece of config will
	// not have an effect.  If an operator later reads the field, it will eventually (but not necessarily immediately)
	// honor the pre-existing spec values.
	ComponentRoutes []ComponentRouteSpec `json:"componentRoutes,omitempty"`
}

type IngressStatus struct {
	// ComponentRoutes is where participating operators place the current route status for routes which the cluster-admin
	// can customize hostnames and serving certificates.
	// How the operator uses that serving certificate is up to the individual operator.
	// An operator that creates entries in this slice should clean them up during removal (if it can be removed).
	// An operator must also handle the case of deleted status without churn.
	ComponentRoutes []ComponentRouteStatus `json:"componentRoutes,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type IngressList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []Ingress `json:"items"`
}

type ComponentRouteSpec struct {
	// namespace is the namespace of the route to customize.  It must be a real namespace.  Using an actual namespace
	// ensures that no two components will conflict and the same component can be installed multiple times.
	// +kubebuilder:validation:Required
	// +required
	Namespace string `json:"namespace"`
	// name is the *logical* name of the route to customize.  It does not have to be the actual name of a route resource.
	// Keep in mind that this is your API for users to customize.  You could later rename the route, but you cannot rename
	// this name.
	// +kubebuilder:validation:Required
	// +required
	Name string `json:"name"`
	// Hostname is the host name that a cluster-admin wants to specify
	Hostname string `json:"hostname,omitempty"`
	// ServingCertKeyPairSecret is a reference to a secret in namespace/openshift-config that is a kubernetes tls secret.
	// The serving cert/key pair must match and will be used by the operator to fulfill the intent of serving with this name.
	// That means it could be embedded into the route or used by the operand directly.
	// Operands should take care to ensure that if they use passthrough termination, they properly use SNI to allow service
	// DNS access to continue to function correctly.
	// SANs in the certificate are ignored, but SNI can be used to make operator managed certificates (like internal load balancers
	// and service serving certificates) serve correctly.
	ServingCertKeyPairSecret SecretNameReference `json:"servingCertKeyPairSecret,omitempty"`
	// possible future, we could add a set of SNI mappings.  I suspect most operators would not properly handle it today.
}

type ComponentRouteStatus struct {
	// namespace is the namespace of the route to customize.  It must be a real namespace.  Using an actual namespace
	// ensures that no two components will conflict and the same component can be installed multiple times.
	Namespace string `json:"namespace,omitempty"`
	// name is the *logical* name of the route to customize.  It does not have to be the actual name of a route resource.
	// Keep in mind that this is your API for users to customize.  You could later rename the route, but you cannot rename
	// this name.
	Name string `json:"name,omitempty"`
	// defaultHostname is the normal host name of this route.  It is provided in case cluster-admins find it more recognizeable
	// and having it here makes it possible to answer, "if I remove my configuration, what will the name be".
	DefaultHostname string `json:"defaultHostname,omitempty"`
	// ConsumingUsers is a slice of users that need to have read permission on the secrets in order to use them.
	// This will usually be an operator service account.
	ConsumingUsers []string `json:"consumingUsers,omitempty"`
	// currentHostnames is the current name used to by the route.  Routes can have more than one exposed name, even though we
	// only allow one route.spec.host.
	CurrentHostnames []string `json:"currentHostnames,omitempty"`

	// conditions are degraded and progressing.  This allows consistent reporting back and feedback that is well
	// structured.  These particular conditions have worked very well in ClusterOperators.
	// Degraded == true means that something has gone wrong trying to handle the ComponentRoute.  The CurrentHostnames
	// may or may not be operating successfully.
	// Progressing == true means that the component is taking some action related to the ComponentRoute
	Conditions []ClusterOperatorStatusCondition `json:"configCondition,omitempty"`

	// relatedObjects allows listing resources which are useful when debugging or inspecting how this is applied.
	// They may be aggregated into an overall status RelatedObjects to be automatically shown by oc adm inspect
	RelatedObjects []corev1.ObjectReference `json:"rrelatedObjects,omitempty"`

	// This API does not include a mechanism to distribute trust, since the ability to write this resource would then
	// allow interception.  Instead, if we need such a mechanism, we can talk about creating a way to allow narrowly scoped
	// updates to a configmap containing ca-bundle.crt for each ComponentRoute.
	// CurrentCABundle []byte
}
