package capture

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
)

const (
	// InjectAnnotation marks a pod that already received capture MITM trust
	// and proxy injection, so the webhook is idempotent.
	InjectAnnotation = "sentinel.karimzakzouk.github.io/capture-mitm"
	// InjectValue is the annotation/namespace label value for opted-in inject.
	InjectValue = "inject"
	// TrustVolume is the volume name that mounts the capture CA.
	TrustVolume = "sentinel-capture-mitm-ca"
	// TrustConfigMap is the per-namespace ConfigMap holding ca.crt and ca.p12.
	TrustConfigMap = "sentinel-capture-mitm-ca"
	// TrustCAPath is where containers find the capture CA PEM.
	TrustCAPath = "/etc/ssl/certs/sentinel-capture-ca.pem"
	// TrustStorePath is the PKCS#12 truststore for JVM clients.
	TrustStorePath = "/etc/ssl/certs/sentinel-capture-ca.p12"
	// TrustStorePassword is Java's conventional PKCS#12 password.
	TrustStorePassword = "changeit"
	// DefaultProxyURL is the in-cluster CONNECT MITM Service.
	DefaultProxyURL = "http://sentinel-capture-mitm.sentinel-capture.svc:8888"
)

var trustEnv = []string{
	"SSL_CERT_FILE",
	"REQUESTS_CA_BUNDLE",
	"NODE_EXTRA_CA_CERTS",
	"CURL_CA_BUNDLE",
	"AWS_CA_BUNDLE",
}

// InjectOptions controls whether HTTPS_PROXY is set (CONNECT) or only the
// Capture CA is mounted (transparent redirect).
type InjectOptions struct {
	ProxyURL  string
	TrustOnly bool // when true, do not set HTTP(S)_PROXY
}

// InjectMITM mounts the capture CA and optionally points HTTP(S)_PROXY at the
// MITM when those variables are unset. Existing values are left alone.
// Idempotent when the trust volume is already present.
func InjectMITM(spec *corev1.PodSpec, proxyURL string) {
	InjectMITMOpts(spec, InjectOptions{ProxyURL: proxyURL})
}

// InjectMITMOpts is InjectMITM with explicit options.
func InjectMITMOpts(spec *corev1.PodSpec, opts InjectOptions) {
	if spec == nil {
		return
	}
	proxyURL := opts.ProxyURL
	if proxyURL == "" {
		proxyURL = DefaultProxyURL
	}
	for _, v := range spec.Volumes {
		if v.Name == TrustVolume {
			return
		}
	}
	spec.Volumes = append(spec.Volumes, corev1.Volume{
		Name: TrustVolume,
		VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: TrustConfigMap},
			Items: []corev1.KeyToPath{
				{Key: "ca.crt", Path: "sentinel-capture-ca.pem"},
				{Key: "ca.p12", Path: "sentinel-capture-ca.p12"},
			},
		}},
	})
	jvmOpts := "-Djavax.net.ssl.trustStore=" + TrustStorePath +
		" -Djavax.net.ssl.trustStorePassword=" + TrustStorePassword +
		" -Djavax.net.ssl.trustStoreType=PKCS12"
	for _, cs := range [][]corev1.Container{spec.InitContainers, spec.Containers} {
		for i := range cs {
			c := &cs[i]
			c.VolumeMounts = append(c.VolumeMounts,
				corev1.VolumeMount{Name: TrustVolume, MountPath: TrustCAPath, SubPath: "sentinel-capture-ca.pem", ReadOnly: true},
				corev1.VolumeMount{Name: TrustVolume, MountPath: TrustStorePath, SubPath: "sentinel-capture-ca.p12", ReadOnly: true},
			)
			for _, name := range trustEnv {
				if hasEnv(c.Env, name) {
					continue
				}
				c.Env = append(c.Env, corev1.EnvVar{Name: name, Value: TrustCAPath})
			}
			if !hasEnv(c.Env, "JAVA_TOOL_OPTIONS") {
				c.Env = append(c.Env, corev1.EnvVar{Name: "JAVA_TOOL_OPTIONS", Value: jvmOpts})
			} else if i := envIndex(c.Env, "JAVA_TOOL_OPTIONS"); i >= 0 && c.Env[i].ValueFrom == nil {
				if !strings.Contains(c.Env[i].Value, "sentinel-capture-ca.p12") {
					c.Env[i].Value = strings.TrimSpace(c.Env[i].Value + " " + jvmOpts)
				}
			}
			if opts.TrustOnly {
				continue
			}
			for _, proxy := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
				if hasEnv(c.Env, proxy) {
					continue
				}
				c.Env = append(c.Env, corev1.EnvVar{Name: proxy, Value: proxyURL})
			}
		}
	}
}

func hasEnv(env []corev1.EnvVar, name string) bool {
	return envIndex(env, name) >= 0
}

func envIndex(env []corev1.EnvVar, name string) int {
	for i, e := range env {
		if e.Name == name {
			return i
		}
	}
	return -1
}
