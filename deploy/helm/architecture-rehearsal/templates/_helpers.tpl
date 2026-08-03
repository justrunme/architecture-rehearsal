{{- define "architecture-rehearsal.fullname" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "architecture-rehearsal.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "architecture-rehearsal.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}
