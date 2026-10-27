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
{{- $_ := set $e "MAX_RUNS_PER_USER" (toString $l.maxRunsPerUser) -}}
{{- $_ := set $e "VALUES_MAX_PER_USER" (toString .Values.valueScripts.maxPerUser) -}}
{{- $_ := set $e "VALUES_MAX_GLOBAL" (toString .Values.valueScripts.maxGlobal) -}}
{{- $_ := set $e "VALUES_CACHE_TTL" (toString .Values.valueScripts.cacheTTL) -}}
{{- $_ := set $e "ACTIVITY_MAX_DAY_BYTES" (toString .Values.activity.maxDayBytes) -}}
{{- $_ := set $e "LOGIN_MAX_FAILURES_PER_IP" (toString $a.maxFailuresPerIP) -}}
{{- $_ := set $e "LOGIN_MAX_FAILURES_GLOBAL" (toString $a.maxFailuresGlobal) -}}
{{- $_ := set $e "SESSION_TTL" (toString $a.sessionTTL) -}}
{{- with $a.publicUrl }}{{ $_ := set $e "PUBLIC_URL" . }}{{ end -}}
{{- if not (kindIs "invalid" $a.cookieSecure) }}{{ $_ := set $e "COOKIE_SECURE" (toString $a.cookieSecure) }}{{ end -}}
{{- range $old := list "adminUsers" "adminGroups" "bootstrapDevops" -}}
{{- if index $a $old -}}
{{- fail (printf "auth.%s was removed: set auth.initialAdmin=true, sign in as the built-in administrator and give the devops role to the first user in the Admin area" $old) -}}
{{- end -}}
{{- end -}}
{{- include "runwarden.runSettings" (dict "root" . "env" $e) -}}
{{- if and .Values.kube.environments (ne (toString .Values.runs.home) "per-run") -}}
{{- fail "kube.environments needs runs.home=per-run: the kubeconfig is written into the run's private HOME" -}}
{{- end -}}
{{- with .Values.executors -}}
{{- $hashes := dict -}}
{{- range $env, $x := . }}{{ $_ := set $hashes $env (include "runwarden.executorTokenSha256" (dict "root" $ "env" $env)) }}{{ end -}}
{{- $_ := set $e "EXECUTORS" (toJson $hashes) -}}
{{- end -}}
{{- $p := .Values.policy -}}
{{- with $p.environments }}{{ $_ := set $e "POLICY_ENVIRONMENTS" (toJson .) }}{{ end -}}
{{- with $p.unknownEnvironment }}{{ $_ := set $e "POLICY_UNKNOWN_ENVIRONMENT" . }}{{ end -}}
{{- $_ := set $e "POLICY_MAX_EXCEPTION_DAYS" (toString $p.maxExceptionDays) -}}
{{- with .Values.allowedOrigins }}{{ $_ := set $e "ALLOWED_ORIGINS" (join "," .) }}{{ end -}}
{{- with .Values.trustedProxies }}{{ $_ := set $e "TRUSTED_PROXIES" (join "," .) }}{{ end -}}
{{- $_ := set $e "HISTORY_MAX_AGE" (toString .Values.history.maxAge) -}}
{{- $_ := set $e "HISTORY_MAX_RUNS" (toString .Values.history.maxRuns) -}}
{{- if .Values.workspace.enabled }}{{ $_ := set $e "WORKSPACE_DIR" "/var/lib/runwarden-work" }}{{ end -}}
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
{{- include "runwarden.envList" $e }}
{{- end -}}

{{- define "runwarden.envList" -}}
{{- range $k, $v := . }}
- name: {{ $k }}
  value: {{ $v | quote }}
{{- end }}
{{- end -}}


{{/* Settings of how scripts run, shared by the server and the executors: (dict "root" . "env" $dict). */}}
{{- define "runwarden.runSettings" -}}
{{- $v := .root.Values -}}
{{- $e := .env -}}
{{- $l := $v.limits -}}
{{- $_ := set $e "KILL_GRACE" (toString $l.killGrace) -}}
{{- $_ := set $e "MAX_OUTPUT_BYTES" (toString $l.maxOutputBytes) -}}
{{- $_ := set $e "MAX_UPLOAD_BYTES" (toString $l.maxUploadBytes) -}}
{{- $_ := set $e "RUN_HOME" (toString $v.runs.home) -}}
{{- if $v.runs.homeSkeletonConfigMap }}{{ $_ := set $e "HOME_SKELETON" "/runwarden/home-skeleton" }}{{ end -}}
{{- $_ := set $e "HEALTH_ADDR" ":8081" -}}
{{- $_ := set $e "SECRETS_DIR" "/runwarden/secrets" -}}
{{- $_ := set $e "KUBE_DIR" "/runwarden/kube" -}}
{{- $iso := $v.isolation -}}
{{- $_ := set $e "ISOLATION" (toString $iso.mode) -}}
{{- with $iso.readPaths }}{{ $_ := set $e "ISOLATION_READ_PATHS" (join "," .) }}{{ end -}}
{{- with $iso.writePaths }}{{ $_ := set $e "ISOLATION_WRITE_PATHS" (join "," .) }}{{ end -}}
{{- $_ := set $e "RUN_MAX_PROCS" (toString $iso.maxProcs) -}}
{{- end -}}

{{/* Kubernetes name part of an environment: lower case, "_" and "." become "-". */}}
{{- define "runwarden.envSlug" -}}
{{- . | lower | replace "_" "-" | replace "." "-" | trunc 30 | trimSuffix "-" -}}
{{- end -}}

{{/* Executor resource name: (dict "root" . "env" "<environment>"). */}}
{{- define "runwarden.executorName" -}}
{{- printf "%s-executor-%s" (include "runwarden.fullname" .root | trunc 32 | trimSuffix "-") (include "runwarden.envSlug" .env) -}}
{{- end -}}

{{- define "runwarden.executorSelectorLabels" -}}
app.kubernetes.io/name: {{ .root.Chart.Name }}-executor
app.kubernetes.io/instance: {{ .root.Release.Name }}
runwarden/environment: {{ include "runwarden.envSlug" .env }}
{{- end -}}

{{- define "runwarden.executorLabels" -}}
{{ include "runwarden.executorSelectorLabels" . }}
app.kubernetes.io/version: {{ .root.Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .root.Chart.Name .root.Chart.Version | replace "+" "_" }}
{{- end -}}

{{/*
Token of an environment's executor, generated once and kept across upgrades (read back from its
Secret). Every template of one render sees the same value: it is kept in .Values.
*/}}
{{- define "runwarden.executorToken" -}}
{{- $root := .root -}}
{{- if not (hasKey $root.Values "__executorTokens") -}}
{{- $_ := set $root.Values "__executorTokens" dict -}}
{{- end -}}
{{- $tokens := index $root.Values "__executorTokens" -}}
{{- if not (hasKey $tokens .env) -}}
{{- $token := randAlphaNum 48 -}}
{{- with lookup "v1" "Secret" $root.Release.Namespace (include "runwarden.executorName" .) -}}
{{- with index .data "token" }}{{ $token = b64dec . }}{{ end -}}
{{- end -}}
{{- $_ := set $tokens .env $token -}}
{{- end -}}
{{- index $tokens .env -}}
{{- end -}}

{{/* Hex SHA-256 of an environment's executor token, as the server's EXECUTORS wants it. */}}
{{- define "runwarden.executorTokenSha256" -}}
{{- $x := index .root.Values.executors .env | default dict -}}
{{- $t := $x.token | default dict -}}
{{- if $t.existingSecret -}}
{{- if not (regexMatch "^[0-9a-fA-F]{64}$" ($t.sha256 | default "")) -}}
{{- fail (printf "executors.%s.token.sha256 is required with token.existingSecret: the hex SHA-256 of the token (printf %%s \"$TOKEN\" | sha256sum)" .env) -}}
{{- end -}}
{{- $t.sha256 | lower -}}
{{- else -}}
{{- include "runwarden.executorToken" . | sha256sum -}}
{{- end -}}
{{- end -}}
