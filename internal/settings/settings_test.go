package settings

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
)

func TestApplyUpdatesActorKillSwitchAndCaptureBodies(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "sentinel-config", Namespace: "sentinel"},
			Data: map[string]string{"config.yaml": "namespaces: [shop]\nledger:\n  path: /var/lib/sentinel/ledger.db\nlogLevel: info\ndetection:\n  falseAlarmsPerWeek: 1\n"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "sentinel-actor-config", Namespace: "sentinel-actor"},
			Data: map[string]string{"config.yaml": "namespaces: [shop]\nenabled: true\nperHour: 6\n"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "sentinel-capture", Namespace: "sentinel-capture"},
			Data: map[string]string{"obi.yaml": "discovery:\n  instrument:\n    - k8s_namespace: shop\n"}},
	)
	on, off := true, false
	bodies := true
	buf := int32(4096)
	perHour := int32(3)
	got, err := Apply(ctx, client, DefaultTargets("sentinel"), v1alpha1.SentinelSettingsSpec{
		Watch:   v1alpha1.WatchSettings{Namespaces: []string{"shop"}},
		Actor:   v1alpha1.ActorSettings{ActionsEnabled: &on, PerHour: &perHour},
		Capture: v1alpha1.CaptureSettings{ExportBodies: &bodies, BodyBufferBytes: &buf},
		Lab:     v1alpha1.LabSettings{VerifyRollbacks: &off},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Installed.Actor || !got.Installed.Capture || got.Installed.Fixer {
		t.Fatalf("installed = %+v", got.Installed)
	}
	actor, err := client.CoreV1().ConfigMaps("sentinel-actor").Get(ctx, "sentinel-actor-config", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(actor.Data["config.yaml"], "perHour: 3") || !strings.Contains(actor.Data["config.yaml"], "enabled: true") {
		t.Fatalf("actor config = %s", actor.Data["config.yaml"])
	}
	ctrl, err := client.CoreV1().ConfigMaps("sentinel").Get(ctx, "sentinel-config", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ctrl.Data["config.yaml"], "verifyRollbacks: false") {
		t.Fatalf("controller config = %s", ctrl.Data["config.yaml"])
	}
	if strings.Contains(ctrl.Data["config.yaml"], "logLevel:") {
		t.Fatalf("controller config retained removed logLevel: %s", ctrl.Data["config.yaml"])
	}
	cap, err := client.CoreV1().ConfigMaps("sentinel-capture").Get(ctx, "sentinel-capture", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cap.Data["obi.yaml"], "http.request.body.content") || !strings.Contains(cap.Data["obi.yaml"], "http: 4096") {
		t.Fatalf("capture config = %s", cap.Data["obi.yaml"])
	}
}
