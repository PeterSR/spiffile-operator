// Package controller reconciles ServiceIdentity objects: it generates each
// identity's keypair into a Secret and maintains per-namespace trust bundle
// ConfigMaps aggregating the public keys of every identity in a trust domain.
package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/PeterSR/spiffile-operator/api/v1alpha1"
	identity "github.com/PeterSR/spiffile/go"
)

const (
	// BundleConfigMapName is the per-namespace ConfigMap holding trust
	// bundles, one data key per trust domain: "<trust-domain>.json".
	BundleConfigMapName = "spiffile-bundle"

	secretIDKey  = "id"
	secretKeyKey = "key.pem"

	defaultRotationOverlap = 24 * time.Hour
	managedByLabel         = "app.kubernetes.io/managed-by"
	managedByValue         = "spiffile-operator"
)

// Reconciler reconciles ServiceIdentity objects.
type Reconciler struct {
	client.Client
}

// SetupWithManager wires the reconciler into the manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.ServiceIdentity{}).
		Owns(&corev1.Secret{}).
		Complete(r)
}

// Reconcile ensures the identity Secret and rebuilds trust bundles.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var si v1alpha1.ServiceIdentity
	if err := r.Get(ctx, req.NamespacedName, &si); err != nil {
		if apierrors.IsNotFound(err) {
			// Identity deleted — drop it from the bundles.
			return ctrl.Result{}, r.rebuildBundles(ctx)
		}
		return ctrl.Result{}, err
	}

	spiffeID, err := spiffeIDFor(&si)
	if err != nil {
		return ctrl.Result{}, r.setFailed(ctx, &si, err)
	}

	// Trust domain exclusivity: a trust domain is either cluster-backed
	// (ServiceIdentity) or externally-backed (claims) — never both.
	claimed, err := r.claimedTrustDomains(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	if claimed[si.Spec.TrustDomain] {
		return ctrl.Result{}, r.setFailed(ctx, &si, fmt.Errorf(
			"trust domain conflict: %q has ServiceIdentityClaim objects; a trust domain is either cluster-backed or externally-backed, never both",
			si.Spec.TrustDomain))
	}

	rotated, err := r.ensureSecret(ctx, &si)
	if err != nil {
		return ctrl.Result{}, r.setFailed(ctx, &si, err)
	}
	if rotated {
		logger.Info("rotated key", "spiffeID", spiffeID)
	}

	jwk, err := r.currentJWK(ctx, &si)
	if err != nil {
		return ctrl.Result{}, r.setFailed(ctx, &si, err)
	}

	pruneExpiredKeys(&si)

	si.Status.SpiffeID = spiffeID
	si.Status.KeyID = jwk.Kid
	si.Status.Ready = true
	si.Status.Message = ""
	if err := r.Status().Update(ctx, &si); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.rebuildBundles(ctx); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue to prune rotated-out keys when the earliest overlap expires.
	if requeue := earliestExpiry(&si); requeue != nil {
		return ctrl.Result{RequeueAfter: time.Until(*requeue) + time.Second}, nil
	}
	return ctrl.Result{}, nil
}

// ensureSecret creates the identity Secret if missing and rotates the key
// when the rotate annotation changes. Returns whether a rotation happened.
func (r *Reconciler) ensureSecret(ctx context.Context, si *v1alpha1.ServiceIdentity) (bool, error) {
	name := secretNameFor(si)
	spiffeID, _ := spiffeIDFor(si)

	var secret corev1.Secret
	err := r.Get(ctx, types.NamespacedName{Namespace: si.Namespace, Name: name}, &secret)
	if apierrors.IsNotFound(err) {
		pemBytes, err := identity.GeneratePrivateKeyPEM()
		if err != nil {
			return false, err
		}
		secret = corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: si.Namespace,
				Labels:    map[string]string{managedByLabel: managedByValue},
			},
			Data: map[string][]byte{
				secretIDKey:  []byte(spiffeID + "\n"),
				secretKeyKey: pemBytes,
			},
		}
		if err := controllerutil.SetControllerReference(si, &secret, r.Scheme()); err != nil {
			return false, err
		}
		return false, r.Create(ctx, &secret)
	}
	if err != nil {
		return false, err
	}

	rotateValue := si.Annotations[v1alpha1.RotateAnnotation]
	if rotateValue == "" || rotateValue == si.Status.LastRotateValue {
		// Keep the id file in sync in case spec.path changed.
		if string(secret.Data[secretIDKey]) != spiffeID+"\n" {
			secret.Data[secretIDKey] = []byte(spiffeID + "\n")
			return false, r.Update(ctx, &secret)
		}
		return false, nil
	}

	// Rotation: keep the old public key in the bundle for the overlap window.
	oldJWK, err := identity.PublicJWKFromPEM(secret.Data[secretKeyKey])
	if err != nil {
		return false, fmt.Errorf("reading key being rotated: %w", err)
	}
	oldJWKJSON, err := json.Marshal(oldJWK)
	if err != nil {
		return false, err
	}

	pemBytes, err := identity.GeneratePrivateKeyPEM()
	if err != nil {
		return false, err
	}
	secret.Data[secretKeyKey] = pemBytes
	secret.Data[secretIDKey] = []byte(spiffeID + "\n")
	if err := r.Update(ctx, &secret); err != nil {
		return false, err
	}

	overlap := defaultRotationOverlap
	if si.Spec.RotationOverlap != nil {
		overlap = si.Spec.RotationOverlap.Duration
	}
	si.Status.PreviousKeys = append(si.Status.PreviousKeys, v1alpha1.PreviousKey{
		JWK:       string(oldJWKJSON),
		ExpiresAt: metav1.NewTime(time.Now().Add(overlap)),
	})
	si.Status.LastRotateValue = rotateValue
	return true, nil
}

// currentJWK derives the identity's current public JWK from its Secret.
func (r *Reconciler) currentJWK(ctx context.Context, si *v1alpha1.ServiceIdentity) (identity.JWK, error) {
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: si.Namespace, Name: secretNameFor(si)}, &secret); err != nil {
		return identity.JWK{}, err
	}
	return identity.PublicJWKFromPEM(secret.Data[secretKeyKey])
}

// claimedTrustDomains returns the trust domains owned by claims (externally
// backed) — the SI side must never write bundle keys for those.
func (r *Reconciler) claimedTrustDomains(ctx context.Context) (map[string]bool, error) {
	var claims v1alpha1.ServiceIdentityClaimList
	if err := r.List(ctx, &claims); err != nil {
		return nil, err
	}
	domains := map[string]bool{}
	for i := range claims.Items {
		domains[claims.Items[i].Spec.TrustDomain] = true
	}
	return domains, nil
}

// rebuildBundles recomputes every cluster-backed trust bundle and writes the
// per-namespace ConfigMaps. Bundles aggregate cluster-wide per trust domain;
// each namespace receives the bundles for the trust domains its identities
// belong to. Data keys owned by claims (externally-backed trust domains) are
// preserved, never overwritten.
func (r *Reconciler) rebuildBundles(ctx context.Context) error {
	var list v1alpha1.ServiceIdentityList
	if err := r.List(ctx, &list); err != nil {
		return err
	}
	claimed, err := r.claimedTrustDomains(ctx)
	if err != nil {
		return err
	}

	bundles := map[string]*identity.Bundle{}         // trust domain -> bundle
	namespaceDomains := map[string]map[string]bool{} // namespace -> trust domains

	for i := range list.Items {
		si := &list.Items[i]
		if claimed[si.Spec.TrustDomain] {
			continue // conflicted; surfaced on its own reconcile
		}
		spiffeID, err := spiffeIDFor(si)
		if err != nil {
			continue // invalid spec; surfaced on its own reconcile
		}
		jwk, err := r.currentJWK(ctx, si)
		if err != nil {
			continue // secret not created yet; surfaced on its own reconcile
		}
		jwkJSON, err := json.Marshal(jwk)
		if err != nil {
			return err
		}

		keys := []json.RawMessage{jwkJSON}
		now := time.Now()
		for _, prev := range si.Status.PreviousKeys {
			if prev.ExpiresAt.After(now) {
				keys = append(keys, json.RawMessage(prev.JWK))
			}
		}

		domain := si.Spec.TrustDomain
		if bundles[domain] == nil {
			bundles[domain] = &identity.Bundle{
				TrustDomain:     domain,
				SpiffileVersion: identity.BundleVersion,
				Identities:      map[string]identity.KeySet{},
			}
		}
		bundles[domain].Identities[spiffeID] = identity.KeySet{Keys: keys}

		if namespaceDomains[si.Namespace] == nil {
			namespaceDomains[si.Namespace] = map[string]bool{}
		}
		namespaceDomains[si.Namespace][domain] = true
	}

	for namespace, domains := range namespaceDomains {
		data := map[string]string{}
		for domain := range domains {
			doc, err := identity.MarshalBundle(*bundles[domain])
			if err != nil {
				return err
			}
			// One deterministic data key per trust domain — never an alias
			// whose presence depends on what else lives in the namespace.
			data[domain+".json"] = string(doc)
		}
		if err := r.upsertBundleConfigMap(ctx, namespace, data, claimed); err != nil {
			return err
		}
	}
	return nil
}

func (r *Reconciler) upsertBundleConfigMap(
	ctx context.Context, namespace string, data map[string]string, claimedDomains map[string]bool,
) error {
	var cm corev1.ConfigMap
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: BundleConfigMapName}, &cm)
	if apierrors.IsNotFound(err) {
		cm = corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      BundleConfigMapName,
				Namespace: namespace,
				Labels:    map[string]string{managedByLabel: managedByValue},
			},
			Data: data,
		}
		return r.Create(ctx, &cm)
	}
	if err != nil {
		return err
	}
	// Preserve data keys owned by claims; replace/remove only cluster-backed keys.
	for key, value := range cm.Data {
		domain := strings.TrimSuffix(key, ".json")
		if claimedDomains[domain] {
			data[key] = value
		}
	}
	if equalStringMaps(cm.Data, data) {
		return nil
	}
	cm.Data = data
	return r.Update(ctx, &cm)
}

func (r *Reconciler) setFailed(ctx context.Context, si *v1alpha1.ServiceIdentity, cause error) error {
	si.Status.Ready = false
	si.Status.Message = cause.Error()
	if err := r.Status().Update(ctx, si); err != nil {
		return err
	}
	return cause
}

// -- helpers ----------------------------------------------------------------

func secretNameFor(si *v1alpha1.ServiceIdentity) string {
	return si.SecretName()
}

func spiffeIDFor(si *v1alpha1.ServiceIdentity) (string, error) {
	path := si.Spec.Path
	if path == "" {
		path = si.Name
	}
	path = strings.TrimPrefix(path, "/")
	// Strict shared validation from the spiffile library — identical rules
	// across implementations, so an identity the operator accepts can never
	// brick a consumer's bundle parse.
	id, err := identity.ParseSpiffeID(fmt.Sprintf("spiffe://%s/%s", si.Spec.TrustDomain, path))
	if err != nil {
		return "", err
	}
	if id.Path == "" {
		return "", fmt.Errorf("identity path must not be empty")
	}
	return id.String(), nil
}

func pruneExpiredKeys(si *v1alpha1.ServiceIdentity) {
	now := time.Now()
	kept := si.Status.PreviousKeys[:0]
	for _, prev := range si.Status.PreviousKeys {
		if prev.ExpiresAt.After(now) {
			kept = append(kept, prev)
		}
	}
	si.Status.PreviousKeys = kept
}

func earliestExpiry(si *v1alpha1.ServiceIdentity) *time.Time {
	var earliest *time.Time
	for _, prev := range si.Status.PreviousKeys {
		t := prev.ExpiresAt.Time
		if earliest == nil || t.Before(*earliest) {
			earliest = &t
		}
	}
	return earliest
}

func equalStringMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
