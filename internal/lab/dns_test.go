package lab

import (
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestTheZoneAnswersProductionNamesWithLabAddresses(t *testing.T) {
	backend := &corev1.Service{Spec: corev1.ServiceSpec{ClusterIP: "10.96.77.77"}}
	backend.Name = "backend"
	headless := &corev1.Service{Spec: corev1.ServiceSpec{ClusterIP: corev1.ClusterIPNone}}
	headless.Name = "peers"
	zone := Zone([]*corev1.Service{headless, backend}, "shop", "cluster.local")
	for _, want := range []string{
		"backend. IN A 10.96.77.77", "backend.shop. IN A 10.96.77.77",
		"backend.shop.svc. IN A 10.96.77.77", "backend.shop.svc.cluster.local. IN A 10.96.77.77",
	} {
		if !strings.Contains(zone, want+"\n") {
			t.Errorf("the zone lacks %q:\n%s", want, zone)
		}
	}
	if strings.Contains(zone, "peers") || !strings.HasPrefix(zone, "$TTL 5\n. IN SOA ") {
		t.Fatalf("unexpected zone:\n%s", zone)
	}
}

// What the Lab DNS logged when Alpine and Debian looked up a copied
// Service and an outside name: only the outside name, as written, is a
// name the world missed.
func TestUnresolvedNamesAreWhatTheWorldAskedFor(t *testing.T) {
	log := `.:1053
CoreDNS-1.14.6
[INFO] A backend.shop.svc.cluster.local. NOERROR
[INFO] AAAA backend.shop.svc.cluster.local.shop.svc.cluster.local. NXDOMAIN
[INFO] A api.stripe.com.shop.svc.cluster.local. NXDOMAIN
[INFO] A api.stripe.com.svc.cluster.local. NXDOMAIN
[INFO] A api.stripe.com.cluster.local. NXDOMAIN
[INFO] A api.stripe.com. NXDOMAIN
[INFO] AAAA api.stripe.com. NXDOMAIN
[INFO] A kubernetes.default.svc.cluster.local. NXDOMAIN
[INFO] A payments. NXDOMAIN
`
	searches := DNSConfig("10.96.0.53", "shop", "cluster.local").Searches
	got := Unresolved(log, searches, []string{"kubernetes.default.svc.cluster.local"})
	if !slices.Equal(got, []string{"api.stripe.com", "payments"}) {
		t.Fatalf("got %v", got)
	}
}
