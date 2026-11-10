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

{{- define "sentinel.watchList" -}}
{{- if .Values.watch.namespaces -}}
{{- .Values.watch.namespaces | toJson -}}
{{- else if .Values.global.watchNamespaces -}}
{{- .Values.global.watchNamespaces | toJson -}}
{{- else -}}
[]
{{- end -}}
{{- end }}

{{- define "sentinel.labNamespaces" -}}
{{- $out := list -}}
{{- if (or .Values.lab.enabled (eq .Values.profile "full")) -}}
{{- $prefix := .Values.lab.namespacePrefix | default "sentinel-lab-" -}}
{{- $size := int .Values.lab.poolSize -}}
{{- range $i := until $size -}}
{{- $out = append $out (printf "%s%d" $prefix $i) -}}
{{- end -}}
{{- end -}}
{{- toJson $out -}}
{{- end }}

{{- define "sentinel.config" -}}
namespaces: {{ include "sentinel.watchList" . }}
agentNamespace: {{ .Release.Namespace }}
labNamespaces: {{ include "sentinel.labNamespaces" . }}
{{- if (or .Values.lab.enabled (eq .Values.profile "full")) }}
verifyRollbacks: {{ .Values.lab.verifyRollbacks }}
{{- end }}
{{- if .Values.fixer.enabled }}
fixerNamespace: {{ .Release.Namespace }}
{{- if .Values.fixer.code.enabled }}
codeFixWithoutRollback: {{ .Values.fixer.code.withoutRollback }}
{{- end }}
{{- end }}
{{- if (or .Values.actor.enabled (eq .Values.profile "full")) }}
actorUser: system:serviceaccount:{{ .Values.actor.namespace }}:{{ .Values.actor.serviceAccount }}
{{- end }}
ledger:
  path: /var/lib/sentinel/ledger.db
capabilities:
  interval: {{ .Values.capabilities.interval }}
detection:
  falseAlarmsPerWeek: {{ .Values.detection.falseAlarmsPerWeek }}
  window: {{ .Values.detection.window }}
  tick: {{ .Values.detection.tick }}
  halfLife: {{ .Values.detection.halfLife }}
  incidentGap: {{ .Values.detection.incidentGap }}
  warmUp: {{ .Values.detection.warmUp }}
logs:
  enabled: {{ .Values.logs.enabled }}
  sampleInterval: {{ .Values.logs.sampleInterval }}
{{- if .Values.capture.enabled }}
capture:
  address: ":4318"
  tokenFile: /etc/sentinel-capture/token
{{- end }}
{{- if .Values.nodeAgent.enabled }}
nodeAgent:
  address: ":4319"
  tokenFile: /etc/sentinel-node-agent/token
{{- end }}
{{- with .Values.console.oidc }}
{{- if .issuer }}
ui:
  oidc:
    issuer: {{ .issuer | quote }}
    clientID: {{ .clientID | quote }}
    {{- if .clientSecret.secretName }}
    clientSecretFile: /etc/sentinel-oidc/{{ .clientSecret.key }}
    {{- end }}
    redirectURL: {{ .redirectURL | quote }}
    scopes: {{ toJson .scopes }}
{{- end }}
{{- end }}
healthAddr: ":8081"
{{- end }}

{{- define "sentinel.captureConfig" -}}
discovery:
  instrument:
    {{- range (include "sentinel.watchList" . | fromJsonArray) }}
    - k8s_namespace: {{ . | quote }}
    {{- end }}
{{- if .Values.capture.exportBodies }}
ebpf:
  buffer_sizes:
    http: {{ int .Values.capture.bodyBufferBytes }}
  payload_extraction:
    http:
      enrichment:
        enabled: true
        policy:
          default_action:
            headers: exclude
            body: exclude
        rules:
          - action: include
            type: body
            scope: request
            match:
              methods: [POST, PUT, PATCH, DELETE]
              url_path_patterns:
                - "*"
          - action: include
            type: body
            scope: response
            match:
              methods: [POST, PUT, PATCH, DELETE]
              url_path_patterns:
                - "*"
attributes:
  select:
    traces:
      include:
        - http.request.body.content
        - http.response.body.content
{{- end }}
{{- end }}

{{- define "sentinel.validate" -}}
{{- $watch := include "sentinel.watchList" . | fromJsonArray -}}
{{- if not $watch -}}
{{- fail "watch.namespaces is empty: list the namespaces Sentinel may watch (or set global.watchNamespaces)" -}}
{{- end -}}
{{- if not (has .Values.profile (list "observe" "full")) -}}
{{- fail (printf "profile is %q: use observe or full" .Values.profile) -}}
{{- end -}}
{{- $own := list .Release.Namespace -}}
{{- if (or .Values.actor.enabled (eq .Values.profile "full")) -}}
{{- $own = append $own .Values.actor.namespace -}}
{{- if eq .Values.actor.namespace .Release.Namespace -}}
{{- fail "actor.namespace must differ from the release namespace, so the controller can never run a pod as the actor" -}}
{{- end -}}
{{- end -}}
{{- $own = concat $own (include "sentinel.labNamespaces" . | fromJsonArray) -}}
{{- if not (has .Values.lab.seal (list "auto" "policy" "firewall")) -}}
{{- fail (printf "lab.seal is %q: use auto, policy or firewall" .Values.lab.seal) -}}
{{- end -}}
{{- range $watch -}}
{{- if has . $own -}}
{{- fail (printf "namespace %q cannot be both watched and one of Sentinel's own" .) -}}
{{- end -}}
{{- end -}}
{{- end }}

{{/*
A worker's replicas and rollout: one, replaced by Recreate, or several that
hold a Lease in turn, rolled one at a time. Takes the worker's values.
*/}}
{{- define "sentinel.workerRollout" -}}
replicas: {{ int .replicas }}
strategy:
{{- if gt (int .replicas) 1 }}
  type: RollingUpdate
  rollingUpdate: {maxUnavailable: 1, maxSurge: 0}
{{- else }}
  type: Recreate
{{- end }}
{{- end }}

{{/*
A worker's Lease, in its config, when it runs more than one replica. Takes
a dict of the worker's values, its namespace and its Lease's name.
*/}}
{{- define "sentinel.leaderElection" -}}
{{- if gt (int .values.replicas) 1 }}
leaderElection:
  namespace: {{ .namespace }}
  name: {{ .name }}
{{- end }}
{{- end }}

{{/*
Lets a worker that runs more than one replica hold its Lease. Takes a dict
of the worker's values, its namespace, its Lease's name, its service
account and the chart's labels.
*/}}
{{- define "sentinel.leaseRBAC" -}}
{{- if gt (int .values.replicas) 1 }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: {{ .name }}-lease
  namespace: {{ .namespace }}
  labels:
    {{- .labels | nindent 4 }}
rules:
  - apiGroups: [coordination.k8s.io]
    resources: [leases]
    verbs: [create]
  - apiGroups: [coordination.k8s.io]
    resources: [leases]
    resourceNames: [{{ .name }}]
    verbs: [get, update]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: {{ .name }}-lease
  namespace: {{ .namespace }}
  labels:
    {{- .labels | nindent 4 }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: {{ .name }}-lease
subjects:
  - kind: ServiceAccount
    name: {{ .serviceAccount }}
    namespace: {{ .namespace }}
{{- end }}
{{- end }}

{{/* The pod's own name, which a worker holds its Lease as. */}}
{{- define "sentinel.podNameEnv" -}}
- name: POD_NAME
  valueFrom:
    fieldRef:
      fieldPath: metadata.name
{{- end }}
