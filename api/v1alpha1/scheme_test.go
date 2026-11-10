package v1alpha1

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

func TestAddToSchemeRegistersEveryKind(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{
		"SentinelPolicy", "SentinelPolicyList",
		"SentinelSettings", "SentinelSettingsList",
		"ActionRequest", "ActionRequestList",
		"Experiment", "ExperimentList",
		"Fix", "FixList",
	} {
		if !s.Recognizes(GroupVersion.WithKind(kind)) {
			t.Errorf("scheme does not recognize %s", kind)
		}
	}
}
