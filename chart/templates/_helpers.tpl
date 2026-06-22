{{/*
Common helpers for the Sentinel chart.
*/}}

{{- define "sentinel.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "sentinel.fullname" -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if .Values.fullnameOverride -}}
{{- $name = .Values.fullnameOverride -}}
{{- end -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "sentinel.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels for every Sentinel resource.
*/}}
{{- define "sentinel.labels" -}}
helm.sh/chart: {{ include "sentinel.chart" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: sentinel
{{- end -}}

{{/*
Labels for a specific service (e.g. ingestion, detection).
Usage: include "sentinel.serviceLabels" (list "service" "ingestion")
*/}}
{{- define "sentinel.serviceLabels" -}}
{{- $service := index . 0 -}}
{{- $root := index . 1 -}}
{{ include "sentinel.labels" $root }}
app.kubernetes.io/name: {{ $service }}
app.kubernetes.io/component: {{ $service }}
{{- end -}}

{{/*
Image reference: registry/image:tag
If tag is empty or "latest", uses the chart's appVersion automatically.
This ensures chart v1.4.2 pulls image v1.4.2 — no more version mismatches.
*/}}
{{- define "sentinel.image" -}}
{{- $root := index . 0 -}}
{{- $svc := index . 1 -}}
{{- $cfg := index $root.Values $svc -}}
{{- $tag := $cfg.tag -}}
{{- if or (eq $tag "") (eq $tag "latest") -}}
{{- $tag = $root.Chart.AppVersion -}}
{{- end -}}
{{- if $root.Values.image.registry -}}
{{- printf "%s/%s:%s" $root.Values.image.registry $cfg.image $tag -}}
{{- else -}}
{{- printf "%s:%s" $cfg.image $tag -}}
{{- end -}}
{{- end -}}

{{/*
Standard env vars shared by all Python services.
*/}}
{{- define "sentinel.commonEnv" -}}
- name: SENTINEL_KAFKA_BROKERS
  value: {{ .Values.kafka.bootstrapServers | quote }}
- name: SENTINEL_POSTGRES_URL
  value: {{ printf "postgresql://%s:%s@%s-postgresql:5432/%s" .Values.postgres.auth.username .Values.postgres.auth.password .Release.Name .Values.postgres.auth.database | quote }}
- name: SENTINEL_WEAVIATE_URL
  value: "http://weaviate:8080"
- name: SENTINEL_OPA_URL
  value: "http://localhost:8181"
- name: SENTINEL_LLM_PROVIDER
  value: {{ .Values.sentinel.llm.provider | quote }}
- name: SENTINEL_LLM_BASE_URL
  value: {{ .Values.sentinel.llm.baseUrl | quote }}
- name: SENTINEL_LLM_API_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "sentinel.fullname" . }}-secrets
      key: llmApiKey
- name: SENTINEL_LLM_FAST_MODEL
  value: {{ .Values.sentinel.llm.fastModel | quote }}
- name: SENTINEL_LLM_FRONTIER_MODEL
  value: {{ .Values.sentinel.llm.frontierModel | quote }}
- name: SENTINEL_GITHUB_TOKEN
  valueFrom:
    secretKeyRef:
      name: {{ include "sentinel.fullname" . }}-secrets
      key: githubToken
- name: SENTINEL_DOCKER_HUB_TOKEN
  valueFrom:
    secretKeyRef:
      name: {{ include "sentinel.fullname" . }}-secrets
      key: dockerHubToken
      optional: true
- name: SENTINEL_DOCKER_HUB_USERNAME
  value: {{ .Values.sentinel.dockerHubUsername | default "" | quote }}
- name: SENTINEL_PRODUCTION_NAMESPACES
  value: {{ .Values.sentinel.productionNamespaces | quote }}
- name: SENTINEL_WRITE_NAMESPACES
  value: {{ join "," .Values.sentinel.rbac.writeNamespaces | quote }}
- name: SENTINEL_CONFIDENCE_THRESHOLD_AUTONOMOUS
  value: {{ .Values.sentinel.confidenceThresholdAutonomous | quote }}
- name: SENTINEL_CONFIDENCE_THRESHOLD_HUMAN_REVIEW
  value: {{ .Values.sentinel.confidenceThresholdHumanReview | quote }}
- name: SENTINEL_PANIC_CPU_THRESHOLD_PCT
  value: {{ .Values.sentinel.panicCpuThresholdPct | quote }}
- name: SENTINEL_PANIC_CONNECTIONS_THRESHOLD
  value: {{ .Values.sentinel.panicConnectionsThreshold | quote }}
{{- end -}}

{{/*
Standard volume mounts for the Python services
*/}}
{{- define "sentinel.standardVolumes" -}}
- name: tmp
  emptyDir: {}
{{- end -}}

{{- define "sentinel.standardVolumeMounts" -}}
- name: tmp
  mountPath: /tmp
{{- end -}}
