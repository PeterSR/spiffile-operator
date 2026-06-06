// Package v1alpha1 contains the spiffile.io/v1alpha1 API types.
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupVersion is the API group/version for the spiffile types.
var GroupVersion = schema.GroupVersion{Group: "spiffile.io", Version: "v1alpha1"}

// RotateAnnotation triggers a key rotation when its value changes.
const RotateAnnotation = "spiffile.io/rotate"

// ServiceIdentitySpec declares a workload identity in a trust domain.
type ServiceIdentitySpec struct {
	// TrustDomain is the SPIFFE trust domain, e.g. "example.org".
	TrustDomain string `json:"trustDomain"`

	// Path is the SPIFFE ID path without leading slash, e.g. "billing".
	// Defaults to the object name.
	Path string `json:"path,omitempty"`

	// SecretName is the name of the Secret holding the identity material
	// (data keys "id" and "key.pem"). Defaults to "<name>-spiffile".
	SecretName string `json:"secretName,omitempty"`

	// RotationOverlap is how long a rotated-out public key remains in the
	// trust bundle so in-flight tokens and stale bundle copies keep
	// verifying. Defaults to 24h.
	RotationOverlap *metav1.Duration `json:"rotationOverlap,omitempty"`
}

// PreviousKey is a rotated-out public key kept in the bundle until ExpiresAt.
type PreviousKey struct {
	// JWK is the JSON-encoded public JWK.
	JWK string `json:"jwk"`
	// ExpiresAt is when the key is pruned from the trust bundle.
	ExpiresAt metav1.Time `json:"expiresAt"`
}

// ServiceIdentityStatus is the observed state of a ServiceIdentity.
type ServiceIdentityStatus struct {
	SpiffeID        string        `json:"spiffeID,omitempty"`
	KeyID           string        `json:"keyID,omitempty"`
	LastRotateValue string        `json:"lastRotateValue,omitempty"`
	PreviousKeys    []PreviousKey `json:"previousKeys,omitempty"`
	Ready           bool          `json:"ready,omitempty"`
	Message         string        `json:"message,omitempty"`
}

// ServiceIdentity is a workload identity managed by the spiffile operator.
type ServiceIdentity struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ServiceIdentitySpec   `json:"spec"`
	Status ServiceIdentityStatus `json:"status,omitempty"`
}

// SecretName returns the name of the Secret holding this identity's material.
func (in *ServiceIdentity) SecretName() string {
	if in.Spec.SecretName != "" {
		return in.Spec.SecretName
	}
	return in.Name + "-spiffile"
}

// ServiceIdentityList is a list of ServiceIdentity objects.
type ServiceIdentityList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ServiceIdentity `json:"items"`
}

// ObjectKeyRef references one data key in a Secret or ConfigMap.
type ObjectKeyRef struct {
	Name string `json:"name"`
	// Key is the data key. Defaults to "bundle.json".
	Key string `json:"key,omitempty"`
	// Namespace may only name the operator's shared-bundle namespace —
	// where a courier delivers the bundle ONCE and the operator fans it out
	// to every claiming namespace. Defaults to the claim's own namespace.
	Namespace string `json:"namespace,omitempty"`
}

// BundleSource references the externally delivered trust bundle document.
type BundleSource struct {
	SecretRef    *ObjectKeyRef `json:"secretRef,omitempty"`
	ConfigMapRef *ObjectKeyRef `json:"configMapRef,omitempty"`
}

// DeliveredSource is the implemented claim source: identity material is
// HANDED to the operator as ordinary Kubernetes objects, delivered by
// whatever courier you run (External Secrets Operator, scripts, CI):
//   - the claim's Secret (spec.secretName) with data keys "id" and "key.pem"
//   - a Secret or ConfigMap holding the trust bundle document
type DeliveredSource struct {
	// BundleFrom references the externally delivered trust bundle for the
	// trust domain. The operator validates it and mirrors it into the
	// per-namespace spiffile-bundle ConfigMap.
	BundleFrom BundleSource `json:"bundleFrom"`
}

// ClaimSource says where a claim's identity material comes from. Exactly one
// member must be set (discriminated union, VolumeSource-style) — new source
// modes are added as new members without changing the consumer contract:
// whatever the mode, the material lands in the claim's Secret and the
// namespace's spiffile-bundle ConfigMap.
//
// Reserved future members (specced, not implemented):
//
//	parameterStore:  the operator reads a store directly (region, prefix, …)
//	externalSecrets: the operator generates ExternalSecret objects that
//	                 deliver exactly what `delivered` consumes (sugar over
//	                 the same contract)
type ClaimSource struct {
	Delivered *DeliveredSource `json:"delivered,omitempty"`
}

// ServiceIdentityClaimSpec binds an externally provisioned identity into a
// namespace — a claim, not a birth certificate (PV/PVC analogy: the external
// authority's entry is the PV, this is the PVC). It carries no authority
// fields: provisioning, rotation and revocation happen at the writer.
type ServiceIdentityClaimSpec struct {
	// TrustDomain is the SPIFFE trust domain, e.g. "example.org".
	TrustDomain string `json:"trustDomain"`

	// Path is the SPIFFE ID path without leading slash. Defaults to the
	// object name.
	Path string `json:"path,omitempty"`

	// SecretName is a RENDEZVOUS name, not a pointer: it declares the
	// agreed name at which the delivery mechanism must place the key Secret
	// (data keys "id" and "key.pem") in this namespace, and it is the
	// Secret pods mount — identical consumer contract in every source mode.
	// Defaults to "<name>-spiffile". The Secret is externally owned in all
	// cases; the operator only reads and validates it, never writes or
	// garbage-collects it (unlike a ServiceIdentity's Secret, which the
	// operator owns).
	SecretName string `json:"secretName,omitempty"`

	// Source says where the identity material comes from.
	Source ClaimSource `json:"source"`
}

// ServiceIdentityClaimStatus is the observed state of a claim.
type ServiceIdentityClaimStatus struct {
	SpiffeID string `json:"spiffeID,omitempty"`
	KeyID    string `json:"keyID,omitempty"`
	Ready    bool   `json:"ready,omitempty"`
	Message  string `json:"message,omitempty"`
}

// ServiceIdentityClaim binds a store-backed identity into a namespace.
type ServiceIdentityClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ServiceIdentityClaimSpec   `json:"spec"`
	Status ServiceIdentityClaimStatus `json:"status,omitempty"`
}

// SecretName returns the name of the Secret holding this claim's material.
func (in *ServiceIdentityClaim) SecretName() string {
	if in.Spec.SecretName != "" {
		return in.Spec.SecretName
	}
	return in.Name + "-spiffile"
}

// ServiceIdentityClaimList is a list of ServiceIdentityClaim objects.
type ServiceIdentityClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ServiceIdentityClaim `json:"items"`
}

// AddToScheme registers the types with a runtime scheme.
func AddToScheme(scheme *runtime.Scheme) error {
	builder := runtime.NewSchemeBuilder(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion,
			&ServiceIdentity{}, &ServiceIdentityList{},
			&ServiceIdentityClaim{}, &ServiceIdentityClaimList{},
		)
		metav1.AddToGroupVersion(s, GroupVersion)
		return nil
	})
	return builder.AddToScheme(scheme)
}

// DeepCopyObject implements runtime.Object.
func (in *ServiceIdentityClaim) DeepCopyObject() runtime.Object {
	out := &ServiceIdentityClaim{}
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
	if in.Spec.Source.Delivered != nil {
		delivered := *in.Spec.Source.Delivered
		if in.Spec.Source.Delivered.BundleFrom.SecretRef != nil {
			ref := *in.Spec.Source.Delivered.BundleFrom.SecretRef
			delivered.BundleFrom.SecretRef = &ref
		}
		if in.Spec.Source.Delivered.BundleFrom.ConfigMapRef != nil {
			ref := *in.Spec.Source.Delivered.BundleFrom.ConfigMapRef
			delivered.BundleFrom.ConfigMapRef = &ref
		}
		out.Spec.Source.Delivered = &delivered
	}
	out.Status = in.Status
	return out
}

// DeepCopyObject implements runtime.Object.
func (in *ServiceIdentityClaimList) DeepCopyObject() runtime.Object {
	out := &ServiceIdentityClaimList{}
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]ServiceIdentityClaim, len(in.Items))
		for i := range in.Items {
			out.Items[i] = *in.Items[i].DeepCopyObject().(*ServiceIdentityClaim)
		}
	}
	return out
}

// DeepCopyObject implements runtime.Object.
func (in *ServiceIdentity) DeepCopyObject() runtime.Object {
	out := &ServiceIdentity{}
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
	if in.Spec.RotationOverlap != nil {
		overlap := *in.Spec.RotationOverlap
		out.Spec.RotationOverlap = &overlap
	}
	out.Status = in.Status
	if in.Status.PreviousKeys != nil {
		out.Status.PreviousKeys = make([]PreviousKey, len(in.Status.PreviousKeys))
		for i, pk := range in.Status.PreviousKeys {
			out.Status.PreviousKeys[i] = PreviousKey{JWK: pk.JWK, ExpiresAt: *pk.ExpiresAt.DeepCopy()}
		}
	}
	return out
}

// DeepCopyObject implements runtime.Object.
func (in *ServiceIdentityList) DeepCopyObject() runtime.Object {
	out := &ServiceIdentityList{}
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]ServiceIdentity, len(in.Items))
		for i := range in.Items {
			out.Items[i] = *in.Items[i].DeepCopyObject().(*ServiceIdentity)
		}
	}
	return out
}
