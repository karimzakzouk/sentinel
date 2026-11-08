// Package kube builds Kubernetes clients from the same inputs kubectl uses.
package kube

import (
	"fmt"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Flags select the cluster and identity, like kubectl's flags of the same names.
type Flags struct {
	Kubeconfig string
	Context    string
	// As impersonates a user, for example to check Sentinel's own service
	// account permissions from an admin kubeconfig.
	As       string
	AsGroups []string
}

// RESTConfig resolves the flags, the KUBECONFIG environment variable, the
// default kubeconfig, and finally the in-cluster service account.
func (f Flags) RESTConfig(userAgent string) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if f.Kubeconfig != "" {
		rules.ExplicitPath = f.Kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: f.Context}
	overrides.AuthInfo.Impersonate = f.As
	overrides.AuthInfo.ImpersonateGroups = f.AsGroups
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load Kubernetes client config: %w", err)
	}
	cfg.UserAgent = userAgent
	cfg.QPS, cfg.Burst = 20, 40
	return cfg, nil
}

// Clientset builds a typed client.
func (f Flags) Clientset(userAgent string) (kubernetes.Interface, error) {
	cfg, err := f.RESTConfig(userAgent)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}
