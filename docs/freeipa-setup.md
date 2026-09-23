# FreeIPA setup

google2ipa talks to the FreeIPA JSON-RPC API (`https://<server>/ipa/session/json`)
over HTTPS. It needs no LDAP access and no schema changes. The same steps
apply to Red Hat Identity Management.

Run the commands below as a FreeIPA administrator (`kinit admin`).

## 1. Managed group

google2ipa only changes users that belong to this group. Users it creates are
added automatically.

```sh
ipa group-add google2ipa-managed --desc="Users managed by google2ipa"
```

Use another name with `sync.managed_group`. The group must not be one of the
groups in `default_groups` or `group_mapping`.

## 2. Least-privilege service account

```sh
ipa role-add google2ipa --desc="google2ipa sync"
ipa role-add-privilege google2ipa \
  --privileges="User Administrators" \
  --privileges="Group Administrators" \
  --privileges="Stage User Administrators"

# FreeIPA grants no role write access to a user's principal expiration,
# which google2ipa uses to record when it locked an account.
ipa permission-add "google2ipa - Write user principal expiration" \
  --type=user --right=write --attrs=krbprincipalexpiration
ipa privilege-add "google2ipa offboarding" --desc="Record when google2ipa locked a user"
ipa privilege-add-permission "google2ipa offboarding" \
  --permissions="google2ipa - Write user principal expiration"
ipa role-add-privilege google2ipa --privileges="google2ipa offboarding"

ipa user-add google2ipa-svc --first=google2ipa --last=service --password
ipa role-add-member google2ipa --users=google2ipa-svc
```

FreeIPA expires any password an administrator sets, so the first login would
ask for a new one. A service account cannot answer that prompt, so give the
password a far-future expiry:

```sh
ipa user-mod google2ipa-svc --setattr=krbPasswordExpiration=20380101000000Z
```

If your password policy has a maximum lifetime, create a separate policy for
the service account, or rotate the password before it expires.

*User Administrators* covers creating, locking and unlocking users,
*Group Administrators* covers group membership, and *Stage User
Administrators* is needed to move deleted users to the preserved container
(`offboarding.preserve: true`). These exact commands are exercised by the
integration test in CI (`hack/freeipa-up.sh`). FreeIPA protects the `admins` group; mapping a
Google group to `admins` needs the service account to be a member of `admins` itself.

## 3. TLS

Copy the IPA CA certificate to where google2ipa runs and set `freeipa.ca_file`:

```sh
scp ipa.example.com:/etc/ipa/ca.crt ./ipa-ca.crt
```

## What google2ipa writes

| Attribute / action | When |
| --- | --- |
| `user-add --random` (+ `givenname`, `sn`, `cn`, `displayname`, `mail`) | A new Google user appears |
| member of `sync.managed_group` | On create or adopt |
| `nsAccountLock=TRUE` + `krbPrincipalExpiration=<now>` | The user left Google. The expiration also blocks Kerberos and records when the lock happened |
| `nsAccountLock` cleared, `krbPrincipalExpiration` removed | The user is back in Google |
| `user-del --preserve` | `offboarding.delete_after` has passed since the lock |

Accounts that are locked **without** a `krbPrincipalExpiration` count as locked
by an administrator. google2ipa never unlocks or deletes them.

## Large directories

google2ipa lists managed users with an unlimited `user-find` (`sizelimit=0`),
which is still capped by the directory server's size limit for non-root
binds (`nsslapd-sizelimit`, 2000 by default). If the result is truncated,
google2ipa stops with an error before changing anything. With more than 2000
managed users, raise the directory server limit on every IPA server:

```sh
dsconf -D "cn=Directory Manager" ldap://localhost config replace nsslapd-sizelimit=10000
```

## Restoring a deleted user

```sh
ipa user-find --preserved=true
ipa user-undel jdoe
ipa group-add-member google2ipa-managed --users=jdoe
```

Deleting removes all group memberships, so the user comes back locked and
outside the managed group; the last command hands it back to google2ipa. On the
next run, if the user is in Google, it is unlocked and its mapped groups are restored.
Until then google2ipa reports it as an existing unmanaged user.

## Migration

If an earlier tool marked synced users with a custom attribute (for example
`isFromGsuite=TRUE`), add those users to the managed group once. Run this on the
FreeIPA server:

```sh
kinit admin
BASEDN=$(grep -Po '(?<=^basedn = ).*' /etc/ipa/default.conf)
ldapsearch -Y GSSAPI -LLL -b "cn=users,cn=accounts,$BASEDN" '(isFromGsuite=TRUE)' uid \
  | awk '/^uid:/{print $2}' \
  | xargs -n 50 sh -c 'ipa group-add-member google2ipa-managed $(printf -- "--users=%s " "$@")' _
```

Then run `google2ipa --dry-run` and read the plan before the first real run.
Users that were locked by the old tool have no `krbPrincipalExpiration`, so
google2ipa treats them as locked by an administrator and leaves them alone.
To let google2ipa manage them again, set the attribute to the approximate lock date:

```sh
ipa user-mod jdoe --setattr=krbPrincipalExpiration=20260101000000Z
```
