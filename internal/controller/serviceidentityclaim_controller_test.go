package controller

import (
	"context"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/PeterSR/spiffile-operator/api/v1alpha1"
	identity "github.com/PeterSR/spiffile/go"
)

const (
	claimNamespace   = "shop"
	claimTrustDomain = "platform.example"
	sharedNamespace  = "spiffile-system"
)

func claimScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

// deliveredMaterial fabricates what a courier (ESO, scripts) would deliver:
// the identity Secret and a shared bundle ConfigMap.
func deliveredMaterial(t *testing.T, name string) (*corev1.Secret, *corev1.ConfigMap) {
	t.Helper()
	pemBytes, err := identity.GeneratePrivateKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	jwk, err := identity.PublicJWKFromPEM(pemBytes)
	if err != nil {
		t.Fatal(err)
	}
	jwkJSON, _ := json.Marshal(jwk)
	spiffeID := "spiffe://" + claimTrustDomain + "/" + name

	doc, err := identity.MarshalBundle(identity.Bundle{
		TrustDomain:     claimTrustDomain,
		SpiffileVersion: identity.BundleVersion,
		Identities:      map[string]identity.KeySet{spiffeID: {Keys: []json.RawMessage{jwkJSON}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-spiffile", Namespace: claimNamespace},
		Data: map[string][]byte{
			"id":      []byte(spiffeID + "\n"),
			"key.pem": pemBytes,
		},
	}
	bundleCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-bundle", Namespace: sharedNamespace},
		Data:       map[string]string{"bundle.json": string(doc)},
	}
	return secret, bundleCM
}

func claimFor(name string) *v1alpha1.ServiceIdentityClaim {
	return &v1alpha1.ServiceIdentityClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: claimNamespace},
		Spec: v1alpha1.ServiceIdentityClaimSpec{
			TrustDomain: claimTrustDomain,
			Source: v1alpha1.ClaimSource{Delivered: &v1alpha1.DeliveredSource{
				BundleFrom: v1alpha1.BundleSource{
					ConfigMapRef: &v1alpha1.ObjectKeyRef{Name: "shared-bundle", Namespace: sharedNamespace},
				},
			}},
		},
	}
}

func reconcileClaim(t *testing.T, c client.Client, name string) *v1alpha1.ServiceIdentityClaim {
	t.Helper()
	reconciler := &ClaimReconciler{Client: c, SharedBundleNamespace: sharedNamespace}
	_, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: claimNamespace, Name: name},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	var claim v1alpha1.ServiceIdentityClaim
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: claimNamespace, Name: name}, &claim); err != nil {
		t.Fatal(err)
	}
	return &claim
}

func TestClaimHappyPath(t *testing.T) {
	scheme := claimScheme(t)
	secret, bundleCM := deliveredMaterial(t, "tm")
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.ServiceIdentityClaim{}).
		WithObjects(claimFor("tm"), secret, bundleCM).Build()

	claim := reconcileClaim(t, c, "tm")
	if !claim.Status.Ready {
		t.Fatalf("expected Ready, got message: %s", claim.Status.Message)
	}
	if claim.Status.SpiffeID != "spiffe://platform.example/tm" || claim.Status.KeyID == "" {
		t.Errorf("unexpected status: %+v", claim.Status)
	}

	// The bundle is mirrored into the namespace's standard ConfigMap.
	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: claimNamespace, Name: BundleConfigMapName}, &cm); err != nil {
		t.Fatalf("bundle configmap: %v", err)
	}
	if _, ok := cm.Data[claimTrustDomain+".json"]; !ok {
		t.Errorf("expected data key %s.json, got %v", claimTrustDomain, mapKeys(cm.Data))
	}
}

func TestClaimWaitsForDeliveredSecret(t *testing.T) {
	scheme := claimScheme(t)
	_, bundleCM := deliveredMaterial(t, "tm")
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.ServiceIdentityClaim{}).
		WithObjects(claimFor("tm"), bundleCM).Build()

	claim := reconcileClaim(t, c, "tm")
	if claim.Status.Ready || claim.Status.Message == "" {
		t.Fatalf("expected waiting status, got: %+v", claim.Status)
	}
}

func TestClaimRejectsIDMismatch(t *testing.T) {
	scheme := claimScheme(t)
	secret, bundleCM := deliveredMaterial(t, "tm")
	secret.Data["id"] = []byte("spiffe://platform.example/someone-else\n")
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.ServiceIdentityClaim{}).
		WithObjects(claimFor("tm"), secret, bundleCM).Build()

	claim := reconcileClaim(t, c, "tm")
	if claim.Status.Ready {
		t.Fatal("expected NotReady on id mismatch")
	}
}

func TestTrustDomainExclusivity(t *testing.T) {
	scheme := claimScheme(t)
	secret, bundleCM := deliveredMaterial(t, "tm")
	conflictingSI := &v1alpha1.ServiceIdentity{
		ObjectMeta: metav1.ObjectMeta{Name: "local", Namespace: claimNamespace},
		Spec:       v1alpha1.ServiceIdentitySpec{TrustDomain: claimTrustDomain},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.ServiceIdentityClaim{}, &v1alpha1.ServiceIdentity{}).
		WithObjects(claimFor("tm"), secret, bundleCM, conflictingSI).Build()

	claim := reconcileClaim(t, c, "tm")
	if claim.Status.Ready {
		t.Fatal("expected NotReady on trust domain conflict")
	}

	// And symmetrically: the SI reconciler must mark the SI conflicted.
	reconciler := &Reconciler{Client: c}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: claimNamespace, Name: "local"},
	}); err == nil {
		t.Fatal("expected conflict error from SI reconcile")
	}
	var si v1alpha1.ServiceIdentity
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: claimNamespace, Name: "local"}, &si); err != nil {
		t.Fatal(err)
	}
	if si.Status.Ready {
		t.Fatal("expected SI NotReady on trust domain conflict")
	}
}

func TestClaimBundleMergePreservesOtherDomains(t *testing.T) {
	scheme := claimScheme(t)
	secret, bundleCM := deliveredMaterial(t, "tm")
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: BundleConfigMapName, Namespace: claimNamespace},
		Data:       map[string]string{"other.org.json": "{}"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.ServiceIdentityClaim{}).
		WithObjects(claimFor("tm"), secret, bundleCM, existing).Build()

	reconcileClaim(t, c, "tm")

	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: claimNamespace, Name: BundleConfigMapName}, &cm); err != nil {
		t.Fatal(err)
	}
	if _, ok := cm.Data["other.org.json"]; !ok {
		t.Error("claim reconcile must not wipe other trust domains' bundle keys")
	}
	if _, ok := cm.Data[claimTrustDomain+".json"]; !ok {
		t.Error("claim's own bundle key missing")
	}
}

func TestClaimRejectsForeignNamespaceReference(t *testing.T) {
	scheme := claimScheme(t)
	secret, bundleCM := deliveredMaterial(t, "tm")
	claim := claimFor("tm")
	claim.Spec.Source.Delivered.BundleFrom.ConfigMapRef.Namespace = "victim-namespace"
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.ServiceIdentityClaim{}).
		WithObjects(claim, secret, bundleCM).Build()

	got := reconcileClaim(t, c, "tm")
	if got.Status.Ready {
		t.Fatal("expected NotReady for a reference outside the shared-bundle namespace")
	}
}

func mapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestClaimDeletionPrunesBundleKey(t *testing.T) {
	scheme := claimScheme(t)
	secret, bundleCM := deliveredMaterial(t, "tm")
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.ServiceIdentityClaim{}).
		WithObjects(claimFor("tm"), secret, bundleCM).Build()

	reconcileClaim(t, c, "tm") // populates the bundle key

	if err := c.Delete(context.Background(), claimFor("tm")); err != nil {
		t.Fatal(err)
	}
	reconciler := &ClaimReconciler{Client: c, SharedBundleNamespace: sharedNamespace}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: claimNamespace, Name: "tm"},
	}); err != nil {
		t.Fatalf("reconcile after deletion: %v", err)
	}

	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: claimNamespace, Name: BundleConfigMapName}, &cm); err != nil {
		t.Fatal(err)
	}
	if _, ok := cm.Data[claimTrustDomain+".json"]; ok {
		t.Error("deleted claim's bundle key must be pruned")
	}
}
