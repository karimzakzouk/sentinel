{{/*
Generic Deployment template for Sentinel Python services.
Renders a Deployment for the given service name.
Usage: include "sentinel.deployment" (list "ingestion" .)
*/}}
{{- define "sentinel.deployment" -}}
{{- $service := index . 0 -}}
{{- $root := index . 1 -}}
{{- $cfg := index $root.Values $service -}}
{{- if $cfg.enabled -}}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ $root.Release.Name }}-{{ $service }}
  labels:
    {{- include "sentinel.serviceLabels" (list $service $root) | nindent 4 }}
spec:
  replicas: {{ $cfg.replicas }}
  selector:
    matchLabels:
      app.kubernetes.io/name: {{ $service }}
      app.kubernetes.io/instance: {{ $root.Release.Name }}
  template:
    metadata:
      labels:
        app.kubernetes.io/name: {{ $service }}
        app.kubernetes.io/instance: {{ $root.Release.Name }}
        app.kubernetes.io/part-of: sentinel
      annotations:
        # Force rollout on config changes
        checksum/config: {{ include "sentinel.commonEnv" $root | sha256sum }}
    spec:
      serviceAccountName: {{ $root.Values.serviceAccount.name | default (include "sentinel.fullname" $root) }}
      {{- with $root.Values.image.pullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      containers:
        - name: {{ $service }}
          image: {{ include "sentinel.image" (list $root $service) }}
          imagePullPolicy: {{ $root.Values.image.pullPolicy }}
          env:
            {{- include "sentinel.commonEnv" $root | nindent 12 }}
            {{- if eq $service "ingestion" }}
            - name: PROMETHEUS_URL
              value: {{ $cfg.env.prometheusUrl | default "" | quote }}
            - name: LOKI_URL
              value: {{ $cfg.env.lokiUrl | default "" | quote }}
            - name: JAEGER_URL
              value: {{ $cfg.env.jaegerUrl | default "" | quote }}
            - name: K8S_IN_CLUSTER_COLLECTOR
              value: "true"
            {{- end }}
            - name: PORT
              value: "8000"
          ports:
            - containerPort: 8000
              name: http
              protocol: TCP
          livenessProbe:
            httpGet:
              path: /healthz
              port: http
            initialDelaySeconds: 30
            periodSeconds: 10
          readinessProbe:
            httpGet:
              path: /healthz
              port: http
            initialDelaySeconds: 5
            periodSeconds: 5
          volumeMounts:
            {{- include "sentinel.standardVolumeMounts" $root | nindent 12 }}
          resources:
            {{- toYaml $cfg.resources | nindent 12 }}
          {{- with $root.Values.nodeSelector }}
          nodeSelector:
            {{- toYaml . | nindent 12 }}
          {{- end }}
          {{- with $root.Values.tolerations }}
          tolerations:
            {{- toYaml . | nindent 12 }}
          {{- end }}
          {{- with $root.Values.affinity }}
          affinity:
            {{- toYaml . | nindent 12 }}
          {{- end }}
      volumes:
        {{- include "sentinel.standardVolumes" $root | nindent 8 }}
---
apiVersion: v1
kind: Service
metadata:
  name: {{ $root.Release.Name }}-{{ $service }}
  labels:
    {{- include "sentinel.serviceLabels" (list $service $root) | nindent 4 }}
spec:
  type: ClusterIP
  ports:
    - port: 8000
      targetPort: http
      protocol: TCP
      name: http
  selector:
    app.kubernetes.io/name: {{ $service }}
    app.kubernetes.io/instance: {{ $root.Release.Name }}
{{- end -}}
{{- end -}}
