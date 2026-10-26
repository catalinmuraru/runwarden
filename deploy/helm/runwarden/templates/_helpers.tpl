{{- define "runwarden.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "runwarden.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "runwarden.labels" -}}
{{ include "runwarden.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{- define "runwarden.image" -}}
{{- if .Values.image.digest -}}
{{ .Values.image.repository }}@{{ .Values.image.digest }}
{{- else -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}
{{- end -}}

{{- define "runwarden.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- .Values.serviceAccount.name | default (include "runwarden.fullname" .) -}}
{{- else -}}
{{- .Values.serviceAccount.name | default "default" -}}
{{- end -}}
{{- end -}}

{{/* Environment from values: only settings that are set, sorted by name. */}}
{{- define "runwarden.env" -}}
{{- $a := .Values.auth -}}
{{- $type := required "auth.type is required: oidc, gitlab, ldap or none" $a.type -}}
{{- if and (ne $type "none") (not .Values.existingSecret) -}}
{{- fail "existingSecret is required unless auth.type=none (it must contain SESSION_KEY)" -}}
{{- end -}}
{{- $e := dict "AUTH_TYPE" $type -}}
{{- $l := .Values.limits -}}
{{- $_ := set $e "MAX_CONCURRENT_RUNS" (toString $l.maxConcurrentRuns) -}}
{{- $_ := set $e "RUN_TIMEOUT" (toString $l.runTimeout) -}}
{{- $_ := set $e "RUN_TIMEOUT_MAX" (toString $l.runTimeoutMax) -}}
{{- $_ := set $e "KILL_GRACE" (toString $l.killGrace) -}}
{{- $_ := set $e "MAX_OUTPUT_BYTES" (toString $l.maxOutputBytes) -}}
{{- $_ := set $e "MAX_UPLOAD_BYTES" (toString $l.maxUploadBytes) -}}
{{- $_ := set $e "SESSION_TTL" (toString $a.sessionTTL) -}}
{{- with $a.publicUrl }}{{ $_ := set $e "PUBLIC_URL" . }}{{ end -}}
{{- if not (kindIs "invalid" $a.cookieSecure) }}{{ $_ := set $e "COOKIE_SECURE" (toString $a.cookieSecure) }}{{ end -}}
{{- with $a.adminUsers }}{{ $_ := set $e "ADMIN_USERS" (join "," .) }}{{ end -}}
{{- with $a.adminGroups }}{{ $_ := set $e "ADMIN_GROUPS" (join "," .) }}{{ end -}}
{{- with .Values.allowedOrigins }}{{ $_ := set $e "ALLOWED_ORIGINS" (join "," .) }}{{ end -}}
{{- with .Values.trustedProxies }}{{ $_ := set $e "TRUSTED_PROXIES" (join "," .) }}{{ end -}}
{{- $_ := set $e "HISTORY_MAX_AGE" (toString .Values.history.maxAge) -}}
{{- $_ := set $e "HISTORY_MAX_RUNS" (toString .Values.history.maxRuns) -}}
{{- if .Values.workspace.enabled }}{{ $_ := set $e "RUNNER_ENV_RUNWARDEN_WORKSPACE" "/var/lib/runwarden-work" }}{{ end -}}
{{- if eq $type "oidc" -}}
{{- $_ := set $e "OIDC_ISSUER_URL" (required "auth.oidc.issuerUrl is required" $a.oidc.issuerUrl) -}}
{{- $_ := set $e "OIDC_CLIENT_ID" (required "auth.oidc.clientId is required" $a.oidc.clientId) -}}
{{- with $a.oidc.scopes }}{{ $_ := set $e "OIDC_SCOPES" . }}{{ end -}}
{{- with $a.oidc.usernameClaim }}{{ $_ := set $e "OIDC_USERNAME_CLAIM" . }}{{ end -}}
{{- with $a.oidc.groupsClaim }}{{ $_ := set $e "OIDC_GROUPS_CLAIM" . }}{{ end -}}
{{- else if eq $type "gitlab" -}}
{{- with $a.gitlab.url }}{{ $_ := set $e "GITLAB_URL" . }}{{ end -}}
{{- $_ := set $e "GITLAB_CLIENT_ID" (required "auth.gitlab.clientId is required" $a.gitlab.clientId) -}}
{{- else if eq $type "ldap" -}}
{{- $_ := set $e "LDAP_URL" (required "auth.ldap.url is required" $a.ldap.url) -}}
{{- $_ := set $e "LDAP_USER_BASE_DN" (required "auth.ldap.userBaseDN is required" $a.ldap.userBaseDN) -}}
{{- if $a.ldap.startTLS }}{{ $_ := set $e "LDAP_START_TLS" "true" }}{{ end -}}
{{- with $a.ldap.bindDN }}{{ $_ := set $e "LDAP_BIND_DN" . }}{{ end -}}
{{- with $a.ldap.userFilter }}{{ $_ := set $e "LDAP_USER_FILTER" . }}{{ end -}}
{{- with $a.ldap.groupBaseDN }}{{ $_ := set $e "LDAP_GROUP_BASE_DN" . }}{{ end -}}
{{- with $a.ldap.groupFilter }}{{ $_ := set $e "LDAP_GROUP_FILTER" . }}{{ end -}}
{{- with $a.ldap.groupNameAttr }}{{ $_ := set $e "LDAP_GROUP_NAME_ATTR" . }}{{ end -}}
{{- with $a.ldap.userGroupsAttr }}{{ $_ := set $e "LDAP_USER_GROUPS_ATTR" . }}{{ end -}}
{{- end -}}
{{- range $k, $v := $e }}
- name: {{ $k }}
  value: {{ $v | quote }}
{{- end }}
{{- end -}}
