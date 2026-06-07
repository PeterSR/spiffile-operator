package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/PeterSR/spiffile-operator/api/v1alpha1"
)

const siTrustDomain = "cluster.example"

func identityFor(namespace, name string) *v1alpha1.ServiceIdentity {
	return &v1alpha1.ServiceIdentity{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       v1alpha1.ServiceIdentitySpec{TrustDomain: siTrustDomain},
	}
}

func reconcileIdentity(t *testing.T, c client.Client, namespace, name string) {
	t.Helper()
	reconciler := &Reconciler{Client: c}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: namespace, Name: name},
	}); err != nil {
		t.Fatalf("reconcile %s/%s: %v", namespace, name, err)
	}
}

func bundleConfigMap(t *testing.T, c client.Client, namespace string) *corev1.ConfigMap {
	t.Helper()
	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: BundleConfigMapName}, &cm); err != nil {
		t.Fatalf("bundle configmap in %s: %v", namespace, err)
	}
	return &cm
}

// Regression test for the revocation gap: deleting the last ServiceIdentity
// in a namespace must not leave that namespace's bundle ConfigMap carrying
// the revoked identity.
func TestIdentityDeletionPrunesEmptiedNamespaceBundle(t *testing.T) {
	scheme := claimScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.ServiceIdentity{}).
		WithObjects(identityFor("a", "x"), identityFor("b", "y")).Build()

	reconcileIdentity(t, c, "a", "x")
	reconcileIdentity(t, c, "b", "y")

	// Both namespaces share the trust domain, so both bundles carry y.
	for _, namespace := range []string{"a", "b"} {
		doc := bundleConfigMap(t, c, namespace).Data[siTrustDomain+".json"]
		if !strings.Contains(doc, "spiffe://"+siTrustDomain+"/y") {
			t.Fatalf("expected %s bundle to contain y before deletion", namespace)
		}
	}

	// Delete y — the only identity in b — and reconcile the NotFound.
	if err := c.Delete(context.Background(), identityFor("b", "y")); err != nil {
		t.Fatal(err)
	}
	reconcileIdentity(t, c, "b", "y")

	// Namespace a keeps its bundle, rebuilt without y.
	doc := bundleConfigMap(t, c, "a").Data[siTrustDomain+".json"]
	if !strings.Contains(doc, "spiffe://"+siTrustDomain+"/x") {
		t.Error("namespace a bundle must keep x")
	}
	if strings.Contains(doc, "spiffe://"+siTrustDomain+"/y") {
		t.Error("namespace a bundle must drop the revoked y")
	}

	// Namespace b no longer earns the trust domain's key at all.
	if doc, ok := bundleConfigMap(t, c, "b").Data[siTrustDomain+".json"]; ok {
		t.Errorf("emptied namespace b must not keep a stale bundle key, got: %s", doc)
	}
}

// Pruning an emptied namespace must not touch data keys owned by claims —
// those trust domains are externally backed and still earned.
func TestIdentityDeletionPreservesClaimOwnedKeys(t *testing.T) {
	scheme := claimScheme(t)
	claim := &v1alpha1.ServiceIdentityClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "ext", Namespace: "b"},
		Spec:       v1alpha1.ServiceIdentityClaimSpec{TrustDomain: "ext.example"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.ServiceIdentity{}).
		WithObjects(identityFor("b", "y"), claim).Build()

	reconcileIdentity(t, c, "b", "y")

	// A claim's bundle key delivered alongside the cluster-backed one.
	cm := bundleConfigMap(t, c, "b")
	cm.Data["ext.example.json"] = "{}"
	if err := c.Update(context.Background(), cm); err != nil {
		t.Fatal(err)
	}

	if err := c.Delete(context.Background(), identityFor("b", "y")); err != nil {
		t.Fatal(err)
	}
	reconcileIdentity(t, c, "b", "y")

	cm = bundleConfigMap(t, c, "b")
	if _, ok := cm.Data[siTrustDomain+".json"]; ok {
		t.Error("cluster-backed key must be pruned from the emptied namespace")
	}
	if _, ok := cm.Data["ext.example.json"]; !ok {
		t.Error("claim-owned key must survive the prune")
	}
}

// Unmanaged ConfigMaps that happen to share the bundle name must be left
// alone — the prune only rewrites ConfigMaps the operator created.
func TestPruneIgnoresUnmanagedConfigMaps(t *testing.T) {
	scheme := claimScheme(t)
	unmanaged := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: BundleConfigMapName, Namespace: "user-owned"},
		Data:       map[string]string{"whatever.json": "{}"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.ServiceIdentity{}).
		WithObjects(unmanaged).Build()

	// Reconcile a NotFound identity — triggers a full rebuild + prune.
	reconcileIdentity(t, c, "gone", "gone")

	cm := bundleConfigMap(t, c, "user-owned")
	if _, ok := cm.Data["whatever.json"]; !ok {
		t.Error("prune must not rewrite ConfigMaps the operator does not manage")
	}
	if apierrors.IsNotFound(c.Get(context.Background(), types.NamespacedName{Namespace: "user-owned", Name: BundleConfigMapName}, &corev1.ConfigMap{})) {
		t.Error("prune must not delete unmanaged ConfigMaps")
	}
}
