{{- define "ee-operator.name" -}}
ee-operator
{{- end -}}

{{- define "ee-operator.fullname" -}}
{{- printf "%s-%s" .Release.Name (include "ee-operator.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "ee-operator.serviceAccountName" -}}
{{- if .Values.serviceAccount.name -}}
{{- .Values.serviceAccount.name -}}
{{- else -}}
{{- printf "%s-controller-manager" (include "ee-operator.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "ee-operator.labels" -}}
app.kubernetes.io/name: {{ include "ee-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}