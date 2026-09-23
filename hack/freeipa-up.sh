#!/usr/bin/env bash
# Start a throwaway FreeIPA server for integration tests and provision the
# least-privilege google2ipa service account documented in docs/freeipa-setup.md.
#
# IPA_HOST     server FQDN (default ipa.example.test, mapped to 127.0.0.1 via /etc/hosts)
# SKIP_HOSTS=1 do not publish :443 or edit /etc/hosts (e.g. OrbStack: IPA_HOST=g2i-ipa.orb.local)
set -euo pipefail
IMAGE=${IMAGE:-quay.io/freeipa/freeipa-server:almalinux-9}
HOST=${IPA_HOST:-ipa.example.test}
DOMAIN=${HOST#*.}
REALM=$(tr '[:lower:]' '[:upper:]' <<<"$DOMAIN")
PW=Secret123
SVC_PW=Svc-Secret-123

publish=(-p 127.0.0.1:443:443)
[[ ${SKIP_HOSTS:-} == 1 ]] && publish=()

docker rm -f g2i-ipa >/dev/null 2>&1 || true
docker volume rm g2i-ipa-data >/dev/null 2>&1 || true
docker run -d -t --name g2i-ipa -h "$HOST" \
  --cgroupns=host -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
  --sysctl net.ipv6.conf.all.disable_ipv6=0 \
  --read-only --tmpfs /run --tmpfs /tmp -v g2i-ipa-data:/data \
  ${publish[@]+"${publish[@]}"} \
  "$IMAGE" ipa-server-install -U -r "$REALM" -n "$DOMAIN" \
  --ds-password=$PW --admin-password=$PW --no-ntp --no-host-dns --skip-mem-check >/dev/null

echo "waiting for FreeIPA install (5-15 min)..." >&2
timeout 2400 bash -c 'until docker logs g2i-ipa 2>&1 | grep -q "FreeIPA server configured."; do
  if [ -z "$(docker ps -q -f name=g2i-ipa)" ]; then docker logs g2i-ipa 2>&1 | tail -50 >&2; exit 1; fi
  sleep 15; done'

ipa() { docker exec -i g2i-ipa "$@"; }
echo $PW | ipa kinit admin >/dev/null
ipa ipa group-add google2ipa-managed --desc="Users managed by google2ipa" >/dev/null
ipa ipa group-add g2i-staff >/dev/null
ipa ipa role-add google2ipa --desc="google2ipa sync" >/dev/null
ipa ipa role-add-privilege google2ipa --privileges="User Administrators" --privileges="Group Administrators" --privileges="Stage User Administrators" >/dev/null
ipa ipa permission-add "google2ipa - Write user principal expiration" --type=user --right=write --attrs=krbprincipalexpiration >/dev/null
ipa ipa privilege-add "google2ipa offboarding" --desc="Record when google2ipa locked a user" >/dev/null
ipa ipa privilege-add-permission "google2ipa offboarding" --permissions="google2ipa - Write user principal expiration" >/dev/null
ipa ipa role-add-privilege google2ipa --privileges="google2ipa offboarding" >/dev/null
echo "$SVC_PW" | ipa ipa user-add google2ipa-svc --first=google2ipa --last=service --password >/dev/null
ipa ipa user-mod google2ipa-svc --setattr=krbPasswordExpiration=20380101000000Z >/dev/null
ipa ipa role-add-member google2ipa --users=google2ipa-svc >/dev/null
docker cp g2i-ipa:/etc/ipa/ca.crt ./ipa-ca.crt

if [[ ${SKIP_HOSTS:-} != 1 ]] && ! grep -q "$HOST" /etc/hosts; then
  echo "127.0.0.1 $HOST" | sudo tee -a /etc/hosts >/dev/null
fi
echo "export G2I_IT_URL=https://$HOST G2I_IT_USER=google2ipa-svc G2I_IT_PASSWORD=$SVC_PW G2I_IT_CA=$PWD/ipa-ca.crt"
