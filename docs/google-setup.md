# Google Workspace setup

google2ipa reads users and group members through the
[Admin SDK Directory API](https://developers.google.com/admin-sdk/directory)
as a **service account with domain-wide delegation** (DWD). It only needs two
read-only scopes and never writes to Google.

You need a Google Cloud project and a Workspace super admin for step 5.

## 1. Enable the Admin SDK API

```sh
gcloud config set project YOUR_PROJECT
gcloud services enable admin.googleapis.com
```

## 2. Create the service account

```sh
gcloud iam service-accounts create google2ipa --display-name="google2ipa"
SA=google2ipa@YOUR_PROJECT.iam.gserviceaccount.com
```

## 3. Choose how google2ipa authenticates

**Option A: key file** (works anywhere):

```sh
gcloud iam service-accounts keys create sa.json --iam-account=$SA
```

Store `sa.json` as a secret and point `google.credentials_file` at it. Treat
it like a password. Your organization policy may block key creation
(`iam.disableServiceAccountKeyCreation`); in that case use option B.

**Option B: no key, Workload Identity** (GKE, Cloud Run, or any
environment with Application Default Credentials). Allow the workload's
identity to sign tokens as the service account:

```sh
gcloud iam service-accounts add-iam-policy-binding $SA \
  --role=roles/iam.serviceAccountTokenCreator \
  --member="principal://iam.googleapis.com/projects/PROJECT_NUMBER/locations/global/workloadIdentityPools/YOUR_PROJECT.svc.id.goog/subject/ns/NAMESPACE/sa/KSA_NAME"
```

Leave `google.credentials_file` empty and set
`google.service_account_email: google2ipa@YOUR_PROJECT.iam.gserviceaccount.com`.

## 4. Get the service account's client ID

```sh
gcloud iam service-accounts describe $SA --format='value(oauth2ClientId)'
```

## 5. Authorize domain-wide delegation

In the [Admin console](https://admin.google.com): **Security → Access and data
control → API controls → Manage Domain Wide Delegation → Add new**.

- Client ID: the number from step 4
- OAuth scopes (comma-separated):

  ```text
  https://www.googleapis.com/auth/admin.directory.user.readonly,https://www.googleapis.com/auth/admin.directory.group.member.readonly
  ```

## 6. Pick the admin to impersonate

`google.admin_email` must be a Workspace user with an admin role that can
read the users (and groups, if you use `group_mapping`) you want to sync. A
custom role with only *Users → Read* and *Groups → Read* is enough. A
dedicated account, e.g. `google2ipa-reader@example.com`, keeps audit logs clear.

## Troubleshooting

| Error | Cause |
| --- | --- |
| `unauthorized_client` | DWD not configured for this client ID, wrong scopes, or the change has not propagated yet (usually minutes, up to 24 h) |
| `403 Not Authorized to access this resource/api` | `admin_email` has no admin role covering these users/groups |
| `404 Domain not found` / `Resource Not Found: customer` | Wrong `google.customer`; keep the default `my_customer` |
| `invalid_grant` | The key was deleted, or the clock of the host is off by several minutes |

Run `google2ipa --dry-run` after each change. It reads Google and FreeIPA but changes nothing.
