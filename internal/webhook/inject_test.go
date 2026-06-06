package webhook

import (
	"context"
	"encoding/json"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/PeterSR/spiffile-operator/api/v1alpha1"
)

func testScheme(t *testing.T) *runtime.Scheme {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func podRequest(t *testing.T, pod *corev1.Pod) admission.Request {
	raw, err := json.Marshal(pod)
	if err != nil {
		t.Fatal(err)
	}
	return admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Namespace: "shop",
			Object:    runtime.RawExtension{Raw: raw},
		},
	}
}

func billingIdentity() *v1alpha1.ServiceIdentity {
	return &v1alpha1.ServiceIdentity{
		ObjectMeta: metav1.ObjectMeta{Name: "billing", Namespace: "shop"},
		Spec:       v1alpha1.ServiceIdentitySpec{TrustDomain: "example.org"},
	}
}

func TestExplicitAnnotationInjects(t *testing.T) {
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(billingIdentity()).Build()
	injector := NewPodInjector(c, scheme)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "app",
			Namespace:   "shop",
			Annotations: map[string]string{IdentityAnnotation: "billing"},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
	}
	resp := injector.Handle(context.Background(), podRequest(t, pod))
	if !resp.Allowed {
		t.Fatalf("expected allowed, got: %v", resp.Result)
	}
	if len(resp.Patches) == 0 {
		t.Fatal("expected patches, got none")
	}
}

func TestServiceAccountConvention(t *testing.T) {
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(billingIdentity()).Build()
	injector := NewPodInjector(c, scheme)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "app",
			Namespace:   "shop",
			Annotations: map[string]string{InjectAnnotation: "true"},
		},
		Spec: corev1.PodSpec{
			ServiceAccountName: "billing",
			Containers:         []corev1.Container{{Name: "app"}},
		},
	}
	resp := injector.Handle(context.Background(), podRequest(t, pod))
	if !resp.Allowed || len(resp.Patches) == 0 {
		t.Fatalf("expected allowed with patches, got allowed=%v patches=%d", resp.Allowed, len(resp.Patches))
	}
}

func TestMissingIdentityDenied(t *testing.T) {
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	injector := NewPodInjector(c, scheme)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "app",
			Namespace:   "shop",
			Annotations: map[string]string{IdentityAnnotation: "nonexistent"},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
	}
	resp := injector.Handle(context.Background(), podRequest(t, pod))
	if resp.Allowed {
		t.Fatal("expected denial for missing ServiceIdentity")
	}
}

func TestNotOptedInUntouched(t *testing.T) {
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	injector := NewPodInjector(c, scheme)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "shop"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
	}
	resp := injector.Handle(context.Background(), podRequest(t, pod))
	if !resp.Allowed || len(resp.Patches) != 0 {
		t.Fatalf("expected allowed without patches, got allowed=%v patches=%d", resp.Allowed, len(resp.Patches))
	}
}

func TestMutateInjectsEverything(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
	}
	Mutate(pod, "billing-spiffile", "example.org")

	if len(pod.Spec.Volumes) != 2 {
		t.Fatalf("expected 2 volumes, got %d", len(pod.Spec.Volumes))
	}
	if pod.Spec.Volumes[0].Secret.SecretName != "billing-spiffile" {
		t.Errorf("unexpected secret name %q", pod.Spec.Volumes[0].Secret.SecretName)
	}
	container := pod.Spec.Containers[0]
	if len(container.VolumeMounts) != 2 || len(container.Env) != 3 {
		t.Errorf("expected 2 mounts + 3 env vars, got %d/%d", len(container.VolumeMounts), len(container.Env))
	}
	if container.Env[2].Value != "/var/run/spiffile/bundle/example.org.json" {
		t.Errorf("bundle path must be trust-domain explicit, got %q", container.Env[2].Value)
	}
	// Idempotent: a second Mutate must not duplicate anything.
	Mutate(pod, "billing-spiffile", "example.org")
	if len(pod.Spec.Volumes) != 2 || len(pod.Spec.Containers[0].Env) != 3 {
		t.Error("Mutate is not idempotent")
	}
}

func TestClaimResolvedByWebhook(t *testing.T) {
	scheme := testScheme(t)
	claim := &v1alpha1.ServiceIdentityClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "platform-svc", Namespace: "shop"},
		Spec: v1alpha1.ServiceIdentityClaimSpec{
			TrustDomain: "platform.example",
			Source: v1alpha1.ClaimSource{Delivered: &v1alpha1.DeliveredSource{
				BundleFrom: v1alpha1.BundleSource{ConfigMapRef: &v1alpha1.ObjectKeyRef{Name: "shared-bundle"}},
			}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(claim).Build()
	injector := NewPodInjector(c, scheme)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "app",
			Namespace:   "shop",
			Annotations: map[string]string{IdentityAnnotation: "platform-svc"},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
	}
	resp := injector.Handle(context.Background(), podRequest(t, pod))
	if !resp.Allowed || len(resp.Patches) == 0 {
		t.Fatalf("claim must resolve for injection: allowed=%v patches=%d", resp.Allowed, len(resp.Patches))
	}
}
