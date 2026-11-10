package lab

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// labCAVolume is the volume name that mounts the Lab CA into copies.
const labCAVolume = "lab-ca"

// labCAPath is where copies find the Lab CA certificate. SSL_CERT_FILE,
// REQUESTS_CA_BUNDLE and NODE_EXTRA_CA_CERTS point here when the copy did
// not already set them.
const labCAPath = "/etc/ssl/certs/lab-ca.pem"

// trustEnv are the environment variables common HTTPS clients read for an
// extra or replacement CA bundle. Setting them is one portable mechanism;
// runtimes that ignore them (a JVM truststore, certificate pinning) stay
// NOT SURE and are recorded as fidelity notes.
var trustEnv = []string{"SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "NODE_EXTRA_CA_CERTS"}

// injectTrust mounts the Lab CA and sets trust environment variables on a
// copied pod so its HTTPS clients accept Lab-issued fake certificates.
// Existing values of those variables are left alone.
func injectTrust(spec *corev1.PodSpec, note func(string, ...any), ref interface{ String() string }) {
	if spec == nil {
		return
	}
	for _, v := range spec.Volumes {
		if v.Name == labCAVolume {
			return
		}
	}
	spec.Volumes = append(spec.Volumes, corev1.Volume{
		Name: labCAVolume,
		VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: LabCAName,
			Items:      []corev1.KeyToPath{{Key: "ca.crt", Path: "lab-ca.pem"}},
		}},
	})
	kept := map[string]bool{}
	for _, cs := range [][]corev1.Container{spec.InitContainers, spec.Containers} {
		for i := range cs {
			c := &cs[i]
			c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
				Name: labCAVolume, MountPath: labCAPath, SubPath: "lab-ca.pem", ReadOnly: true,
			})
			for _, name := range trustEnv {
				if hasEnv(c.Env, name) {
					kept[name] = true
					continue
				}
				c.Env = append(c.Env, corev1.EnvVar{Name: name, Value: labCAPath})
			}
		}
	}
	note("%s trusts the Lab CA at %s via SSL_CERT_FILE, REQUESTS_CA_BUNDLE and NODE_EXTRA_CA_CERTS where unset; runtimes that pin certificates or use another trust store stay NOT SURE",
		ref, labCAPath)
	for _, name := range trustEnv {
		if kept[name] {
			note("%s already sets %s; the Lab CA was mounted but that variable was left as production set it", ref, name)
		}
	}
}

func hasEnv(env []corev1.EnvVar, name string) bool {
	for _, e := range env {
		if e.Name == name {
			return true
		}
	}
	return false
}

// likelyClientAuth is true when a copy mounts client certificate material
// that the Lab replaced with a honeytoken or emptied, so mutual TLS against
// an HTTPS fake cannot succeed faithfully.
func likelyClientAuth(spec corev1.PodSpec, fidelity []string, ref string) bool {
	prefix := ref + "'s volume "
	for _, f := range fidelity {
		if !strings.HasPrefix(f, prefix) {
			continue
		}
		low := strings.ToLower(f)
		if strings.Contains(low, "honeytoken secret") || strings.Contains(low, "empty directory instead of secret") {
			if strings.Contains(low, "tls") || strings.Contains(low, "cert") || strings.Contains(low, "mtls") || strings.Contains(low, "client") {
				return true
			}
		}
	}
	for _, v := range spec.Volumes {
		name := strings.ToLower(v.Name)
		if !(strings.Contains(name, "tls") || strings.Contains(name, "cert") || strings.Contains(name, "mtls") || strings.Contains(name, "client")) {
			continue
		}
		if v.Name == labCAVolume {
			continue
		}
		if v.Secret != nil || v.EmptyDir != nil {
			return true
		}
	}
	for _, c := range append(append([]corev1.Container{}, spec.InitContainers...), spec.Containers...) {
		for _, e := range c.Env {
			n := strings.ToLower(e.Name)
			if strings.Contains(n, "client_cert") || strings.Contains(n, "client_key") || strings.Contains(n, "mtls") {
				return true
			}
		}
	}
	return false
}
