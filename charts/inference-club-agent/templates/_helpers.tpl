{{- define "agent.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "agent.fullname" -}}
{{- printf "%s-%s" .Release.Name (include "agent.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "agent.labels" -}}
app.kubernetes.io/name: {{ include "agent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "agent.discoveryNamespace" -}}
{{- default .Release.Namespace .Values.discovery.namespace -}}
{{- end -}}

{{- define "agent.apiKeySecretName" -}}
{{- if .Values.apiKey.existingSecret -}}
{{- .Values.apiKey.existingSecret -}}
{{- else -}}
{{- include "agent.fullname" . -}}-api-key
{{- end -}}
{{- end -}}
