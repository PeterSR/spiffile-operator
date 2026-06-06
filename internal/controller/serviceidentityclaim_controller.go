// Package controller: the ServiceIdentityClaim reconciler consumes
// externally delivered identity material (replica mode). The operator never
// talks to any store — a courier (External Secrets Operator, scripts, CI)
// delivers ordinary Secrets/ConfigMaps, and this reconciler validates them,
// mirrors the bundle into the standard spiffile-bundle ConfigMap, and
// reports status. See docs/store-backend.md.
package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/PeterSR/spiffile-operator/api/v1alpha1"
	identity "github.com/PeterSR/spiffile/go"
)

// DefaultBundleKey is the default data key for bundleFrom references.
const DefaultBundleKey = "bundle.json"

// ClaimReconciler reconciles ServiceIdentityClaim objects.
type ClaimReconciler struct {
	client.Client

	// SharedBundleNamespace is the single namespace bundleFrom references may
	// point at cross-namespace: a courier delivers the bundle once there and
	// the operator fans it out. Empty disables cross-namespace references.
	SharedBundleNamespace string
}

// SetupWithManager wires the reconciler into the manager. Delivered
// Secrets/ConfigMaps are watched so courier updates propagate immediately —
// no polling.
func (r *ClaimReconciler) SetupWithManager(mgr ctrl.Manager) error {
	mapToClaims := func(ctx context.Context, obj client.Object) []reconcile.Request {
		var claims v1alpha1.ServiceIdentityClaimList
		var listOptions []client.ListOption
		if obj.GetNamespace() != r.SharedBundleNamespace {
			// Shared-namespace bundle objects may be referenced by claims in
			// ANY namespace; everything else is namespace-local.
			listOptions = append(listOptions, client.InNamespace(obj.GetNamespace()))
		}
		if err := r.List(ctx, &claims, listOptions...); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for i := range claims.Items {
			claim := &claims.Items[i]
			sameNamespace := claim.Namespace == obj.GetNamespace()
			if (sameNamespace && claim.SecretName() == obj.GetName()) || r.referencesObject(claim, obj) {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{Namespace: claim.Namespace, Name: claim.Name},
				})
			}
		}
		return requests
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.ServiceIdentityClaim{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(mapToClaims)).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(mapToClaims)).
		Complete(r)
}

// Reconcile validates the delivered material and mirrors the bundle.
func (r *ClaimReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var claim v1alpha1.ServiceIdentityClaim
	if err := r.Get(ctx, req.NamespacedName, &claim); err != nil {
		if apierrors.IsNotFound(err) {
			// Claim deleted — prune bundle keys its trust domain no longer earns.
			return ctrl.Result{}, r.pruneBundleKeys(ctx, req.Namespace)
		}
		return ctrl.Result{}, err
	}

	spiffeID, err := claimSpiffeID(&claim)
	if err != nil {
		return ctrl.Result{}, r.setClaimFailed(ctx, &claim, err.Error())
	}
	claim.Status.SpiffeID = spiffeID

	// Trust domain exclusivity: a trust domain is either cluster-backed
	// (ServiceIdentity) or externally-backed (claims) — never both.
	conflict, err := r.trustDomainHasIdentities(ctx, claim.Spec.TrustDomain)
	if err != nil {
		return ctrl.Result{}, err
	}
	if conflict {
		return ctrl.Result{}, r.setClaimFailed(ctx, &claim, fmt.Sprintf(
			"trust domain conflict: %q has cluster-backed ServiceIdentity objects; a trust domain is either cluster-backed or externally-backed, never both",
			claim.Spec.TrustDomain))
	}

	delivered := claim.Spec.Source.Delivered
	if delivered == nil {
		return ctrl.Result{}, r.setClaimFailed(ctx, &claim,
			"spec.source.delivered is required (the only implemented source mode)")
	}

	// The delivered identity Secret: data "id" + "key.pem".
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: claim.Namespace, Name: claim.SecretName()}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.setClaimFailed(ctx, &claim, fmt.Sprintf(
				"waiting for secret %q to be delivered", claim.SecretName()))
		}
		return ctrl.Result{}, err
	}
	jwk, err := identity.PublicJWKFromPEM(secret.Data[secretKeyKey])
	if err != nil {
		return ctrl.Result{}, r.setClaimFailed(ctx, &claim, fmt.Sprintf(
			"delivered secret %q: invalid key.pem: %v", claim.SecretName(), err))
	}
	deliveredID := strings.TrimSpace(string(secret.Data[secretIDKey]))
	if deliveredID != spiffeID {
		return ctrl.Result{}, r.setClaimFailed(ctx, &claim, fmt.Sprintf(
			"delivered secret %q: id %q does not match claimed identity %q", claim.SecretName(), deliveredID, spiffeID))
	}

	// The delivered bundle: validate, then mirror into spiffile-bundle.
	doc, sourceDescription, err := r.resolveBundleSource(ctx, &claim)
	if err != nil {
		return ctrl.Result{}, r.setClaimFailed(ctx, &claim, err.Error())
	}
	bundle, warnings, err := identity.ParseBundle(doc)
	if err != nil {
		return ctrl.Result{}, r.setClaimFailed(ctx, &claim, fmt.Sprintf("%s: %v", sourceDescription, err))
	}
	for _, warning := range warnings {
		logger.Info("bundle warning", "claim", claim.Name, "warning", warning)
	}
	if bundle.TrustDomain != claim.Spec.TrustDomain {
		return ctrl.Result{}, r.setClaimFailed(ctx, &claim, fmt.Sprintf(
			"%s: bundle is for trust domain %q, claim is for %q", sourceDescription, bundle.TrustDomain, claim.Spec.TrustDomain))
	}
	if _, ok := bundle.Identities[spiffeID]; !ok {
		return ctrl.Result{}, r.setClaimFailed(ctx, &claim, fmt.Sprintf(
			"%s: bundle has no keys for %q — not provisioned at the authority yet", sourceDescription, spiffeID))
	}
	if err := r.mergeBundleKey(ctx, claim.Namespace, claim.Spec.TrustDomain+".json", string(doc)); err != nil {
		return ctrl.Result{}, err
	}

	claim.Status.KeyID = jwk.Kid
	claim.Status.Ready = true
	claim.Status.Message = ""
	return ctrl.Result{}, r.Status().Update(ctx, &claim)
}

// resolveBundleSource reads the referenced bundle document.
func (r *ClaimReconciler) resolveBundleSource(ctx context.Context, claim *v1alpha1.ServiceIdentityClaim) ([]byte, string, error) {
	source := claim.Spec.Source.Delivered.BundleFrom
	switch {
	case source.SecretRef != nil:
		namespace, err := r.sourceNamespace(claim, source.SecretRef.Namespace)
		if err != nil {
			return nil, "", err
		}
		key := defaultKey(source.SecretRef.Key)
		description := fmt.Sprintf("bundle secret %s/%s key %q", namespace, source.SecretRef.Name, key)
		var secret corev1.Secret
		if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: source.SecretRef.Name}, &secret); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, "", fmt.Errorf("waiting for %s to be delivered", description)
			}
			return nil, "", err
		}
		doc, ok := secret.Data[key]
		if !ok {
			return nil, "", fmt.Errorf("%s: key not present", description)
		}
		return doc, description, nil
	case source.ConfigMapRef != nil:
		namespace, err := r.sourceNamespace(claim, source.ConfigMapRef.Namespace)
		if err != nil {
			return nil, "", err
		}
		key := defaultKey(source.ConfigMapRef.Key)
		description := fmt.Sprintf("bundle configmap %s/%s key %q", namespace, source.ConfigMapRef.Name, key)
		var cm corev1.ConfigMap
		if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: source.ConfigMapRef.Name}, &cm); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, "", fmt.Errorf("waiting for %s to be delivered", description)
			}
			return nil, "", err
		}
		doc, ok := cm.Data[key]
		if !ok {
			return nil, "", fmt.Errorf("%s: key not present", description)
		}
		return []byte(doc), description, nil
	default:
		return nil, "", fmt.Errorf("spec.bundleFrom must reference a secret or a configmap")
	}
}

// sourceNamespace resolves (and authorizes) the namespace of a bundle
// reference. Cross-namespace references are only allowed into the
// shared-bundle namespace — otherwise creating a claim would let anyone
// make the operator copy content out of arbitrary namespaces.
func (r *ClaimReconciler) sourceNamespace(claim *v1alpha1.ServiceIdentityClaim, referenced string) (string, error) {
	if referenced == "" || referenced == claim.Namespace {
		return claim.Namespace, nil
	}
	if referenced == r.SharedBundleNamespace {
		return referenced, nil
	}
	return "", fmt.Errorf(
		"bundleFrom may only reference this namespace or the shared-bundle namespace %q, not %q",
		r.SharedBundleNamespace, referenced)
}

// mergeBundleKey upserts one data key into the namespace's spiffile-bundle
// ConfigMap, preserving keys owned by other trust domains.
func (r *ClaimReconciler) mergeBundleKey(ctx context.Context, namespace, dataKey, doc string) error {
	var cm corev1.ConfigMap
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: BundleConfigMapName}, &cm)
	if apierrors.IsNotFound(err) {
		cm = corev1.ConfigMap{}
		cm.Name = BundleConfigMapName
		cm.Namespace = namespace
		cm.Labels = map[string]string{managedByLabel: managedByValue}
		cm.Data = map[string]string{dataKey: doc}
		return r.Create(ctx, &cm)
	}
	if err != nil {
		return err
	}
	if cm.Data[dataKey] == doc {
		return nil
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[dataKey] = doc
	return r.Update(ctx, &cm)
}

// pruneBundleKeys removes bundle ConfigMap data keys for trust domains that
// no longer have a claim or a ServiceIdentity in the namespace.
func (r *ClaimReconciler) pruneBundleKeys(ctx context.Context, namespace string) error {
	var cm corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: BundleConfigMapName}, &cm); err != nil {
		return client.IgnoreNotFound(err)
	}

	keep := map[string]bool{}
	var claims v1alpha1.ServiceIdentityClaimList
	if err := r.List(ctx, &claims, client.InNamespace(namespace)); err != nil {
		return err
	}
	for i := range claims.Items {
		keep[claims.Items[i].Spec.TrustDomain+".json"] = true
	}
	var identities v1alpha1.ServiceIdentityList
	if err := r.List(ctx, &identities, client.InNamespace(namespace)); err != nil {
		return err
	}
	for i := range identities.Items {
		keep[identities.Items[i].Spec.TrustDomain+".json"] = true
	}

	changed := false
	for key := range cm.Data {
		if !keep[key] {
			delete(cm.Data, key)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return r.Update(ctx, &cm)
}

func (r *ClaimReconciler) trustDomainHasIdentities(ctx context.Context, trustDomain string) (bool, error) {
	var identities v1alpha1.ServiceIdentityList
	if err := r.List(ctx, &identities); err != nil {
		return false, err
	}
	for i := range identities.Items {
		if identities.Items[i].Spec.TrustDomain == trustDomain {
			return true, nil
		}
	}
	return false, nil
}

func (r *ClaimReconciler) setClaimFailed(ctx context.Context, claim *v1alpha1.ServiceIdentityClaim, message string) error {
	claim.Status.Ready = false
	claim.Status.Message = message
	return r.Status().Update(ctx, claim)
}

// -- helpers ------------------------------------------------------------------

func claimSpiffeID(claim *v1alpha1.ServiceIdentityClaim) (string, error) {
	path := claim.Spec.Path
	if path == "" {
		path = claim.Name
	}
	path = strings.TrimPrefix(path, "/")
	id, err := identity.ParseSpiffeID(fmt.Sprintf("spiffe://%s/%s", claim.Spec.TrustDomain, path))
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

func defaultKey(key string) string {
	if key == "" {
		return DefaultBundleKey
	}
	return key
}

func (r *ClaimReconciler) referencesObject(claim *v1alpha1.ServiceIdentityClaim, obj client.Object) bool {
	if claim.Spec.Source.Delivered == nil {
		return false
	}
	matches := func(ref *v1alpha1.ObjectKeyRef) bool {
		if ref == nil || ref.Name != obj.GetName() {
			return false
		}
		namespace, err := r.sourceNamespace(claim, ref.Namespace)
		return err == nil && namespace == obj.GetNamespace()
	}
	bundleFrom := claim.Spec.Source.Delivered.BundleFrom
	return matches(bundleFrom.SecretRef) || matches(bundleFrom.ConfigMapRef)
}
