{{- define "spiffile-operator.name" -}}
{{- .Chart.Name -}}
{{- end -}}

{{- define "spiffile-operator.labels" -}}
app.kubernetes.io/name: {{ include "spiffile-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "spiffile-operator.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}
