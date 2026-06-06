// Package webhook implements optional pod injection: pods that opt in via
// annotation get the spiffile identity volumes, mounts and environment
// variables injected, so workload manifests never reference the generated
// Secret/ConfigMap names.
package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/PeterSR/spiffile-operator/api/v1alpha1"
	"github.com/PeterSR/spiffile-operator/internal/controller"
)

const (
	// IdentityAnnotation names the ServiceIdentity a pod runs as.
	IdentityAnnotation = "spiffile.io/identity"
	// InjectAnnotation opts a pod into injection with the identity
	// defaulting to the pod's service account name.
	InjectAnnotation = "spiffile.io/inject"
	// injectedAnnotation marks a pod as already processed.
	injectedAnnotation = "spiffile.io/injected"

	identityVolumeName = "spiffile-identity"
	bundleVolumeName   = "spiffile-bundle"
	identityMountPath  = "/var/run/spiffile/identity"
	bundleMountPath    = "/var/run/spiffile/bundle"

	envIDFile     = "SPIFFILE_ID_FILE"
	envKeyFile    = "SPIFFILE_KEY_FILE"
	envBundleFile = "SPIFFILE_BUNDLE_FILE"
)

// PodInjector is a mutating admission handler for pod CREATE requests.
type PodInjector struct {
	Client  client.Client
	decoder admission.Decoder
}

// NewPodInjector builds a PodInjector with a decoder for the given scheme.
func NewPodInjector(c client.Client, scheme *runtime.Scheme) *PodInjector {
	return &PodInjector{Client: c, decoder: admission.NewDecoder(scheme)}
}

// Handle implements admission.Handler.
func (i *PodInjector) Handle(ctx context.Context, req admission.Request) admission.Response {
	pod := &corev1.Pod{}
	if err := i.decoder.Decode(req, pod); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	name := identityNameFor(pod)
	if name == "" || pod.Annotations[injectedAnnotation] == "true" {
		return admission.Allowed("not opted in to spiffile injection")
	}

	// Resolve against either kind — a cluster-backed ServiceIdentity or an
	// externally-backed ServiceIdentityClaim. Pods can't tell the difference.
	secretName, trustDomain, err := i.resolveIdentity(ctx, req.Namespace, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return admission.Denied(fmt.Sprintf(
				"pod requests spiffile identity %q but no ServiceIdentity or ServiceIdentityClaim with that name exists in namespace %q",
				name, req.Namespace,
			))
		}
		return admission.Errored(http.StatusInternalServerError, err)
	}

	Mutate(pod, secretName, trustDomain)

	raw, err := json.Marshal(pod)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	return admission.PatchResponseFromRaw(req.Object.Raw, raw)
}

// identityNameFor resolves which ServiceIdentity a pod opted into, or "".
func identityNameFor(pod *corev1.Pod) string {
	if name := pod.Annotations[IdentityAnnotation]; name != "" {
		return name
	}
	if pod.Annotations[InjectAnnotation] == "true" {
		// Convention: identity defaults to the service account name.
		if pod.Spec.ServiceAccountName != "" {
			return pod.Spec.ServiceAccountName
		}
		return "default"
	}
	return ""
}

// resolveIdentity looks the annotation name up as a ServiceIdentity, then as
// a ServiceIdentityClaim, returning the secret name and trust domain.
func (i *PodInjector) resolveIdentity(ctx context.Context, namespace, name string) (string, string, error) {
	var si v1alpha1.ServiceIdentity
	err := i.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &si)
	if err == nil {
		return si.SecretName(), si.Spec.TrustDomain, nil
	}
	if !apierrors.IsNotFound(err) {
		return "", "", err
	}
	var claim v1alpha1.ServiceIdentityClaim
	if err := i.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &claim); err != nil {
		return "", "", err
	}
	return claim.SecretName(), claim.Spec.TrustDomain, nil
}

// Mutate injects the identity volumes, mounts and env vars into a pod.
func Mutate(pod *corev1.Pod, secretName, trustDomain string) {
	addVolume(pod, corev1.Volume{
		Name: identityVolumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: secretName},
		},
	})
	addVolume(pod, corev1.Volume{
		Name: bundleVolumeName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: controller.BundleConfigMapName},
			},
		},
	})

	// The bundle data key is per trust domain — injecting the explicit name
	// means adding identities from other trust domains to the namespace can
	// never break this pod.
	bundleFile := bundleMountPath + "/" + trustDomain + ".json"
	for idx := range pod.Spec.Containers {
		injectContainer(&pod.Spec.Containers[idx], bundleFile)
	}
	for idx := range pod.Spec.InitContainers {
		injectContainer(&pod.Spec.InitContainers[idx], bundleFile)
	}

	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[injectedAnnotation] = "true"
}

func injectContainer(container *corev1.Container, bundleFile string) {
	for _, env := range container.Env {
		if env.Name == envIDFile {
			return // already configured, leave the container alone
		}
	}
	container.VolumeMounts = append(container.VolumeMounts,
		corev1.VolumeMount{Name: identityVolumeName, MountPath: identityMountPath, ReadOnly: true},
		corev1.VolumeMount{Name: bundleVolumeName, MountPath: bundleMountPath, ReadOnly: true},
	)
	container.Env = append(container.Env,
		corev1.EnvVar{Name: envIDFile, Value: identityMountPath + "/id"},
		corev1.EnvVar{Name: envKeyFile, Value: identityMountPath + "/key.pem"},
		corev1.EnvVar{Name: envBundleFile, Value: bundleFile},
	)
}

func addVolume(pod *corev1.Pod, volume corev1.Volume) {
	for _, existing := range pod.Spec.Volumes {
		if existing.Name == volume.Name {
			return
		}
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, volume)
}
