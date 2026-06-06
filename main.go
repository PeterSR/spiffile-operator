// The spiffile operator turns ServiceIdentity objects into identity Secrets
// and per-namespace trust bundle ConfigMaps, following the spiffile profile.
package main

import (
	"os"
	"path/filepath"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/PeterSR/spiffile-operator/api/v1alpha1"
	"github.com/PeterSR/spiffile-operator/internal/controller"
	spiffilewebhook "github.com/PeterSR/spiffile-operator/internal/webhook"
)

// webhookCertDir is controller-runtime's default serving cert location; pod
// injection is enabled automatically when a TLS cert is mounted there.
const webhookCertDir = "/tmp/k8s-webhook-server/serving-certs"

func main() {
	ctrl.SetLogger(zap.New(zap.UseDevMode(false)))
	logger := ctrl.Log.WithName("setup")

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		logger.Error(err, "adding client-go scheme")
		os.Exit(1)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		logger.Error(err, "adding spiffile scheme")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: ":8081",
		LeaderElection:         true,
		LeaderElectionID:       "spiffile-operator.spiffile.io",
		WebhookServer:          webhook.NewServer(webhook.Options{CertDir: webhookCertDir}),
	})
	if err != nil {
		logger.Error(err, "creating manager")
		os.Exit(1)
	}

	if err := (&controller.Reconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "creating controller")
		os.Exit(1)
	}

	// Claims (replica mode): cross-namespace bundle references are allowed
	// only into the shared-bundle namespace — the operator's own by default.
	sharedBundleNamespace := os.Getenv("SHARED_BUNDLE_NAMESPACE")
	if sharedBundleNamespace == "" {
		sharedBundleNamespace = os.Getenv("POD_NAMESPACE")
	}
	if err := (&controller.ClaimReconciler{
		Client:                mgr.GetClient(),
		SharedBundleNamespace: sharedBundleNamespace,
	}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "creating claim controller")
		os.Exit(1)
	}

	// Pod injection is optional: it activates when serving certs are mounted
	// (see deploy/webhook.yaml). Without certs the webhook server never starts.
	if _, err := os.Stat(filepath.Join(webhookCertDir, "tls.crt")); err == nil {
		mgr.GetWebhookServer().Register("/inject-pod", &admission.Webhook{
			Handler: spiffilewebhook.NewPodInjector(mgr.GetClient(), scheme),
		})
		logger.Info("pod injection webhook enabled")
	} else {
		logger.Info("no webhook serving certs found, pod injection disabled")
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Error(err, "adding health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Error(err, "adding ready check")
		os.Exit(1)
	}

	logger.Info("starting spiffile operator")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error(err, "running manager")
		os.Exit(1)
	}
}
