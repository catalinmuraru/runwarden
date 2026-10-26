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
{{- if hasKey $a "type" -}}
{{- fail "auth.type was replaced by auth.methods (a list): e.g. auth.methods: [gitlab]" -}}
{{- end -}}
{{- $methods := $a.methods | default list -}}
{{- $none := has "none" $methods -}}
{{- if and (not $methods) (not $a.initialAdmin) -}}
{{- fail "auth.methods is required: gitlab, oidc and/or ldap (or [none] with auth.allowNoAuth for local testing)" -}}
{{- end -}}
{{- if and (not $none) (not .Values.existingSecret) -}}
{{- fail "existingSecret is required unless auth.methods is [none] (it must contain SESSION_KEY)" -}}
{{- end -}}
{{- if and $none (not $a.allowNoAuth) -}}
{{- fail "auth.methods [none] makes every visitor an admin; set auth.allowNoAuth=true as well to confirm (local testing only)" -}}
{{- end -}}
{{- $e := dict "AUTH_METHODS" (join "," $methods) -}}
{{- if $none }}{{ $_ := set $e "ALLOW_NO_AUTH" "true" }}{{ end -}}
{{- if $a.initialAdmin }}{{ $_ := set $e "INITIAL_ADMIN" "true" }}{{ end -}}
{{- $l := .Values.limits -}}
{{- $_ := set $e "MAX_CONCURRENT_RUNS" (toString $l.maxConcurrentRuns) -}}
{{- $_ := set $e "RUN_TIMEOUT" (toString $l.runTimeout) -}}
{{- $_ := set $e "RUN_TIMEOUT_MAX" (toString $l.runTimeoutMax) -}}
{{- $_ := set $e "KILL_GRACE" (toString $l.killGrace) -}}
{{- $_ := set $e "MAX_RUNS_PER_USER" (toString $l.maxRunsPerUser) -}}
{{- $_ := set $e "RUN_HOME" (toString .Values.runs.home) -}}
{{- if .Values.runs.homeSkeletonConfigMap }}{{ $_ := set $e "HOME_SKELETON" "/etc/runwarden/home-skeleton" }}{{ end -}}
{{- $_ := set $e "VALUES_MAX_PER_USER" (toString .Values.valueScripts.maxPerUser) -}}
{{- $_ := set $e "VALUES_MAX_GLOBAL" (toString .Values.valueScripts.maxGlobal) -}}
{{- $_ := set $e "VALUES_CACHE_TTL" (toString .Values.valueScripts.cacheTTL) -}}
{{- $_ := set $e "ACTIVITY_MAX_DAY_BYTES" (toString .Values.activity.maxDayBytes) -}}
{{- $_ := set $e "LOGIN_MAX_FAILURES_PER_IP" (toString $a.maxFailuresPerIP) -}}
{{- $_ := set $e "LOGIN_MAX_FAILURES_GLOBAL" (toString $a.maxFailuresGlobal) -}}
{{- $_ := set $e "HEALTH_ADDR" ":8081" -}}
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
{{- if has "oidc" $methods -}}
{{- $_ := set $e "OIDC_ISSUER_URL" (required "auth.oidc.issuerUrl is required" $a.oidc.issuerUrl) -}}
{{- $_ := set $e "OIDC_CLIENT_ID" (required "auth.oidc.clientId is required" $a.oidc.clientId) -}}
{{- with $a.oidc.scopes }}{{ $_ := set $e "OIDC_SCOPES" . }}{{ end -}}
{{- with $a.oidc.usernameClaim }}{{ $_ := set $e "OIDC_USERNAME_CLAIM" . }}{{ end -}}
{{- with $a.oidc.groupsClaim }}{{ $_ := set $e "OIDC_GROUPS_CLAIM" . }}{{ end -}}
{{- with $a.oidc.label }}{{ $_ := set $e "OIDC_LABEL" . }}{{ end -}}
{{- with $a.oidc.requiredGroup }}{{ $_ := set $e "OIDC_REQUIRED_GROUP" . }}{{ end -}}
{{- end -}}
{{- if has "gitlab" $methods -}}
{{- with $a.gitlab.url }}{{ $_ := set $e "GITLAB_URL" . }}{{ end -}}
{{- $_ := set $e "GITLAB_CLIENT_ID" (required "auth.gitlab.clientId is required" $a.gitlab.clientId) -}}
{{- with $a.gitlab.label }}{{ $_ := set $e "GITLAB_LABEL" . }}{{ end -}}
{{- with $a.gitlab.requiredGroup }}{{ $_ := set $e "GITLAB_REQUIRED_GROUP" . }}{{ end -}}
{{- end -}}
{{- if has "ldap" $methods -}}
{{- $_ := set $e "LDAP_URL" (required "auth.ldap.url is required" $a.ldap.url) -}}
{{- $_ := set $e "LDAP_USER_BASE_DN" (required "auth.ldap.userBaseDN is required" $a.ldap.userBaseDN) -}}
{{- if $a.ldap.startTLS }}{{ $_ := set $e "LDAP_START_TLS" "true" }}{{ end -}}
{{- if $a.ldap.insecure }}{{ $_ := set $e "LDAP_INSECURE" "true" }}{{ end -}}
{{- with $a.ldap.bindDN }}{{ $_ := set $e "LDAP_BIND_DN" . }}{{ end -}}
{{- with $a.ldap.userFilter }}{{ $_ := set $e "LDAP_USER_FILTER" . }}{{ end -}}
{{- with $a.ldap.groupBaseDN }}{{ $_ := set $e "LDAP_GROUP_BASE_DN" . }}{{ end -}}
{{- with $a.ldap.groupFilter }}{{ $_ := set $e "LDAP_GROUP_FILTER" . }}{{ end -}}
{{- with $a.ldap.groupNameAttr }}{{ $_ := set $e "LDAP_GROUP_NAME_ATTR" . }}{{ end -}}
{{- with $a.ldap.userGroupsAttr }}{{ $_ := set $e "LDAP_USER_GROUPS_ATTR" . }}{{ end -}}
{{- with $a.ldap.label }}{{ $_ := set $e "LDAP_LABEL" . }}{{ end -}}
{{- end -}}
{{- range $k, $v := $e }}
- name: {{ $k }}
  value: {{ $v | quote }}
{{- end }}
{{- end -}}
