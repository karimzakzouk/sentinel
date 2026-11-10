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

// labTrustStorePath is a PKCS#12 truststore of the Lab CA for JVM clients.
const labTrustStorePath = "/etc/ssl/certs/lab-ca.p12"

// trustEnv are the environment variables common HTTPS clients read for an
// extra or replacement CA bundle. Setting them is one portable mechanism;
// certificate pinning still stays NOT SURE.
var trustEnv = []string{"SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "NODE_EXTRA_CA_CERTS", "CURL_CA_BUNDLE", "AWS_CA_BUNDLE"}

// injectTrust mounts the Lab CA (PEM and PKCS#12) and sets trust environment
// variables on a copied pod so its HTTPS clients accept Lab-issued fake
// certificates. Existing values of those variables are left alone, except a
// literal JAVA_TOOL_OPTIONS is appended with Lab truststore opts when missing.
// HTTPS_PROXY rewriting for MITM clients is rewriteProxyEnv's job.
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
			Items: []corev1.KeyToPath{
				{Key: "ca.crt", Path: "lab-ca.pem"},
				{Key: "ca.p12", Path: "lab-ca.p12"},
			},
		}},
	})
	kept := map[string]bool{}
	jvmOpts := "-Djavax.net.ssl.trustStore=" + labTrustStorePath + " -Djavax.net.ssl.trustStorePassword=" + TrustStorePassword + " -Djavax.net.ssl.trustStoreType=PKCS12"
	setAnyTrust := false
	for _, cs := range [][]corev1.Container{spec.InitContainers, spec.Containers} {
		for i := range cs {
			c := &cs[i]
			c.VolumeMounts = append(c.VolumeMounts,
				corev1.VolumeMount{Name: labCAVolume, MountPath: labCAPath, SubPath: "lab-ca.pem", ReadOnly: true},
				corev1.VolumeMount{Name: labCAVolume, MountPath: labTrustStorePath, SubPath: "lab-ca.p12", ReadOnly: true},
			)
			for _, name := range trustEnv {
				if hasEnv(c.Env, name) {
					kept[name] = true
					continue
				}
				c.Env = append(c.Env, corev1.EnvVar{Name: name, Value: labCAPath})
				setAnyTrust = true
			}
			switch {
			case !hasEnv(c.Env, "JAVA_TOOL_OPTIONS"):
				c.Env = append(c.Env, corev1.EnvVar{Name: "JAVA_TOOL_OPTIONS", Value: jvmOpts})
				setAnyTrust = true
			default:
				if e := envVar(c.Env, "JAVA_TOOL_OPTIONS"); e != nil && e.ValueFrom == nil {
					if !strings.Contains(e.Value, labTrustStorePath) {
						e.Value = strings.TrimSpace(e.Value + " " + jvmOpts)
						setAnyTrust = true
					}
				} else {
					kept["JAVA_TOOL_OPTIONS"] = true
				}
			}
		}
	}
	note("%s trusts the Lab CA at %s via SSL_CERT_FILE, REQUESTS_CA_BUNDLE, NODE_EXTRA_CA_CERTS, CURL_CA_BUNDLE, AWS_CA_BUNDLE and a PKCS#12 truststore for the JVM where unset; certificate pinning stays NOT SURE",
		ref, labCAPath)
	for _, name := range append(append([]string{}, trustEnv...), "JAVA_TOOL_OPTIONS") {
		if kept[name] {
			note("%s already sets %s; the Lab CA was mounted but that variable was left as production set it", ref, name)
		}
	}
	if !setAnyTrust {
		note("%s already sets every Lab trust environment variable; the CA was mounted but no trust override was applied", ref)
	}
}

func hasEnv(env []corev1.EnvVar, name string) bool {
	return envVar(env, name) != nil
}

func envVar(env []corev1.EnvVar, name string) *corev1.EnvVar {
	for i := range env {
		if env[i].Name == name {
			return &env[i]
		}
	}
	return nil
}

// hasProxyEnv reports whether any container already sets an HTTP(S)_PROXY value.
func hasProxyEnv(spec corev1.PodSpec) bool {
	for _, c := range append(append([]corev1.Container{}, spec.InitContainers...), spec.Containers...) {
		for _, e := range c.Env {
			if isProxyEnv(e.Name) && e.Value != "" {
				return true
			}
		}
	}
	return false
}

// trustEnvApplied is true when at least one Lab trust variable points at the
// Lab CA path or JAVA_TOOL_OPTIONS names the Lab truststore.
func trustEnvApplied(spec corev1.PodSpec) bool {
	for _, c := range append(append([]corev1.Container{}, spec.InitContainers...), spec.Containers...) {
		for _, e := range c.Env {
			if e.ValueFrom != nil {
				continue
			}
			for _, name := range trustEnv {
				if e.Name == name && e.Value == labCAPath {
					return true
				}
			}
			if e.Name == "JAVA_TOOL_OPTIONS" && strings.Contains(e.Value, labTrustStorePath) {
				return true
			}
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
