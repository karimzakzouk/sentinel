// Package v1alpha1 contains Sentinel's custom resources. They hold intent and
// small status only; incidents, evidence and decisions live in the Ledger,
// because etcd caps objects at 1.5 MiB.
//
// +kubebuilder:object:generate=true
// +groupName=sentinel.karimzakzouk.github.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Group is a DNS name the project controls, as Kubernetes API groups should be.
const Group = "sentinel.karimzakzouk.github.io"

var (
	GroupVersion  = schema.GroupVersion{Group: Group, Version: "v1alpha1"}
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion,
		&SentinelPolicy{}, &SentinelPolicyList{},
		&ActionRequest{}, &ActionRequestList{},
		&Experiment{}, &ExperimentList{},
	)
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
