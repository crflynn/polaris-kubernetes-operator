{{- define "polaris-kubernetes-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "polaris-kubernetes-operator.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "polaris-kubernetes-operator.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | quote }}
app.kubernetes.io/name: {{ include "polaris-kubernetes-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "polaris-kubernetes-operator.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "polaris-kubernetes-operator.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- required "serviceAccount.name is required when serviceAccount.create is false" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "polaris-kubernetes-operator.image" -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}
{{- end -}}

{{- define "polaris-kubernetes-operator.leaderElectionName" -}}
{{- printf "%s-leader-election" (include "polaris-kubernetes-operator.fullname" . | trunc 46 | trimSuffix "-") | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "polaris-kubernetes-operator.metricsServiceName" -}}
{{- printf "%s-metrics" (include "polaris-kubernetes-operator.fullname" . | trunc 55 | trimSuffix "-") | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "polaris-kubernetes-operator.metricsAuthName" -}}
{{- printf "%s-metrics-auth" (include "polaris-kubernetes-operator.fullname" . | trunc 50 | trimSuffix "-") | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "polaris-kubernetes-operator.metricsReaderName" -}}
{{- printf "%s-metrics-reader" (include "polaris-kubernetes-operator.fullname" . | trunc 48 | trimSuffix "-") | trunc 63 | trimSuffix "-" -}}
{{- end -}}
