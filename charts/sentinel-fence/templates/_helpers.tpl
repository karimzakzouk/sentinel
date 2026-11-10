{{- define "fence.labels" -}}
app.kubernetes.io/name: sentinel-fence
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: sentinel
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end }}

{{- define "fence.controllerUser" -}}
system:serviceaccount:{{ .Values.controller.namespace }}:{{ .Values.controller.serviceAccount }}
{{- end }}

{{- define "fence.actorUser" -}}
system:serviceaccount:{{ .Values.actor.namespace }}:{{ .Values.actor.serviceAccount }}
{{- end }}

{{- /* CEL list literal of the namespaces the actor may change. */ -}}
{{- define "fence.namespaces" -}}
[{{ range $i, $ns := .Values.namespaces }}{{ if $i }}, {{ end }}'{{ $ns }}'{{ end }}]
{{- end }}

{{- /* Shared checks for every workload update: replicas, the selector, and the workload's labels and annotations. */ -}}
{{- define "fence.workloadValidations" -}}
- expression: "object.spec.?replicas.orValue(1) >= {{ .Values.replicas.min }} && object.spec.?replicas.orValue(1) <= {{ .Values.replicas.max }}"
  message: "replicas must stay between {{ .Values.replicas.min }} and {{ .Values.replicas.max }}"
- expression: "object.spec.selector == oldObject.spec.selector"
  message: "Sentinel may not change a workload's selector"
- expression: "object.metadata.?labels == oldObject.metadata.?labels"
  message: "Sentinel may not change workload labels"
- expression: >-
    variables.newObjectAnnotations.all(k, k.startsWith('sentinel.karimzakzouk.github.io/') || (k in variables.oldObjectAnnotations && variables.oldObjectAnnotations[k] == variables.newObjectAnnotations[k])) &&
    variables.oldObjectAnnotations.all(k, k.startsWith('sentinel.karimzakzouk.github.io/') || k in variables.newObjectAnnotations)
  message: "Sentinel may only change its own annotations on a workload"
{{- end }}

{{- /* The API server omits empty request fields, and reading a missing key is an error in CEL. */ -}}
{{- define "fence.requestVariables" -}}
- name: subResource
  expression: "request.?subResource.orValue('')"
- name: dryRun
  expression: "request.?dryRun.orValue(false)"
- name: requestNamespace
  expression: "request.?namespace.orValue('')"
{{- end }}

{{- /* CEL: container c of a Lab pod meets the restricted Pod Security level, unless it is the firewall, which its own rule bounds. */ -}}
{{- define "fence.labRestricted" -}}
(c.name == 'lab-seal' || (!c.?securityContext.?privileged.orValue(false) &&
 c.?securityContext.?allowPrivilegeEscalation.orValue(true) == false &&
 c.?securityContext.?capabilities.?drop.orValue([]).exists(d, d == 'ALL') &&
 c.?securityContext.?capabilities.?add.orValue([]).all(a, a == 'NET_BIND_SERVICE') &&
 c.?securityContext.?runAsNonRoot.orValue(object.spec.?securityContext.?runAsNonRoot.orValue(false)) == true &&
 c.?securityContext.?runAsUser.orValue(object.spec.?securityContext.?runAsUser.orValue(1)) != 0 &&
 c.?securityContext.?seccompProfile.?type.orValue(object.spec.?securityContext.?seccompProfile.?type.orValue('')) in ['RuntimeDefault', 'Localhost'] &&
 c.?ports.orValue([]).all(p, p.?hostPort.orValue(0) == 0)))
{{- end }}

{{- define "fence.workloadVariables" -}}
- name: newAnnotations
  expression: "object.spec.template.metadata.?annotations.orValue({})"
- name: oldAnnotations
  expression: "oldObject.spec.template.metadata.?annotations.orValue({})"
- name: newObjectAnnotations
  expression: "object.metadata.?annotations.orValue({})"
- name: oldObjectAnnotations
  expression: "oldObject.metadata.?annotations.orValue({})"
{{- end }}
