{{- define "sentinel.labels" -}}
app.kubernetes.io/name: sentinel
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: sentinel
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end }}

{{- define "sentinel.selectorLabels" -}}
app.kubernetes.io/name: sentinel
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: controller
{{- end }}

{{- define "sentinel.image" -}}
{{- if .Values.image.digest -}}
{{ .Values.image.repository }}@{{ .Values.image.digest }}
{{- else -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}
{{- end }}

{{- define "sentinel.labNamespaces" -}}
{{- $out := list -}}
{{- if .Values.lab.enabled -}}
{{- range $i := until (int .Values.lab.poolSize) -}}
{{- $out = append $out (printf "%s%d" $.Values.lab.namespacePrefix $i) -}}
{{- end -}}
{{- end -}}
{{- toJson $out -}}
{{- end }}

{{- define "sentinel.config" -}}
namespaces: {{ toJson .Values.watch.namespaces }}
agentNamespace: {{ .Release.Namespace }}
labNamespaces: {{ include "sentinel.labNamespaces" . }}
{{- if .Values.actor.enabled }}
actorUser: system:serviceaccount:{{ .Values.actor.namespace }}:{{ .Values.actor.serviceAccount }}
{{- end }}
ledger:
  path: /var/lib/sentinel/ledger.db
capabilities:
  interval: {{ .Values.capabilities.interval }}
healthAddr: ":8081"
{{- end }}

{{- define "sentinel.validate" -}}
{{- if not .Values.watch.namespaces -}}
{{- fail "watch.namespaces is empty: list the namespaces Sentinel may watch" -}}
{{- end -}}
{{- $own := list .Release.Namespace -}}
{{- if .Values.actor.enabled -}}
{{- $own = append $own .Values.actor.namespace -}}
{{- if eq .Values.actor.namespace .Release.Namespace -}}
{{- fail "actor.namespace must differ from the release namespace, so the controller can never run a pod as the actor" -}}
{{- end -}}
{{- end -}}
{{- $own = concat $own (include "sentinel.labNamespaces" . | fromJsonArray) -}}
{{- range .Values.watch.namespaces -}}
{{- if has . $own -}}
{{- fail (printf "namespace %q cannot be both watched and one of Sentinel's own" .) -}}
{{- end -}}
{{- end -}}
{{- end }}
