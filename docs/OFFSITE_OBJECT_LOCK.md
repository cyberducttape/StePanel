# Immutable offsite backups with S3 Object Lock

An attacker who gains control of the panel host also holds the credentials
the panel uses to write offsite backups. Without provider-side immutability,
those credentials can delete every backup before the attacker does damage
that needs one. S3 Object Lock prevents that: each object version is
retained for a fixed period, and in compliance mode no credential, including
the bucket owner's, can delete it before then.

StePanel supports this with `STEPANEL_OFFSITE_OBJECT_LOCK=1`.

## What StePanel does in Object Lock mode

- **Uploads go straight to the final prefix.** Normally a backup is copied to
  a staging prefix, verified, copied to its final prefix, and the staging
  copy is purged. In a locked bucket the staging copy could never be purged
  and would double the retained storage, so StePanel writes the final prefix
  directly. The completion marker is still written last, after verification,
  so a partial upload is never offered for restore. A retried upload of the
  same backup succeeds because its files are unchanged (`--immutable`).
- **Readiness proves the lock.** The `offsite_backup` readiness check writes
  a probe object under `.stepanel-health/lock-<random>/`, deletes it (which
  in a versioned bucket only hides it), and then tries to delete the hidden
  version permanently (`rclone backend cleanup-hidden`) with the panel's own
  credentials. The check passes only if that deletion is refused and the
  version still reads back intact. Any other outcome, including an rclone
  result the check does not recognize, fails readiness rather than vouching
  for an unverified bucket. A passing proof stands for 6 hours; a failing one
  is retried every 30 seconds while readiness is polled. Each proof leaves
  one small object version that the bucket retains for its default
  retention.

StePanel never deletes published offsite backups, so expiry is the bucket's
job: retention ends with the default retention, and a lifecycle rule removes
expired versions.

## Bucket setup (AWS S3)

Object Lock must be enabled when the bucket is created, and it turns on
versioning. Pick a retention long enough to notice a compromise and recover:
30 days is a common floor.

```sh
aws s3api create-bucket --bucket stepanel-backups-prod --region eu-west-1 \
  --create-bucket-configuration LocationConstraint=eu-west-1 \
  --object-lock-enabled-for-bucket
aws s3api put-object-lock-configuration --bucket stepanel-backups-prod \
  --object-lock-configuration '{"ObjectLockEnabled":"Enabled","Rule":{"DefaultRetention":{"Mode":"COMPLIANCE","Days":30}}}'
# Remove versions once their retention has ended, and delete markers.
aws s3api put-bucket-lifecycle-configuration --bucket stepanel-backups-prod \
  --lifecycle-configuration '{"Rules":[{"ID":"expire-unlocked-versions","Status":"Enabled","Filter":{},
    "NoncurrentVersionExpiration":{"NoncurrentDays":31},
    "Expiration":{"ExpiredObjectDeleteMarker":true},
    "AbortIncompleteMultipartUpload":{"DaysAfterInitiation":7}}]}'
```

Use **compliance** mode. In governance mode, a principal with
`s3:BypassGovernanceRetention` can delete locked versions; the readiness proof
does not send the bypass header, so it cannot tell the two modes apart.

Objects in a locked bucket do not expire on their own: a current object
stays until something deletes it, and its versions stay until their
retention ends and the lifecycle rule removes them. To keep only recent
backups, add a lifecycle rule that expires current objects under the backup
prefix after the retention period (`"Expiration":{"Days":30}`); the lock
keeps them undeletable until then.

## Panel credentials (least privilege)

Give the panel an IAM user or role that can write, read and list, and
nothing that can change or bypass the lock:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "StePanelBackups",
      "Effect": "Allow",
      "Action": ["s3:PutObject", "s3:GetObject", "s3:GetObjectVersion", "s3:DeleteObject", "s3:ListBucket", "s3:ListBucketVersions", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"],
      "Resource": ["arn:aws:s3:::stepanel-backups-prod", "arn:aws:s3:::stepanel-backups-prod/*"]
    },
    {
      "Sid": "NeverTheLock",
      "Effect": "Deny",
      "Action": ["s3:BypassGovernanceRetention", "s3:PutObjectRetention", "s3:PutObjectLegalHold", "s3:PutBucketObjectLockConfiguration", "s3:PutBucketVersioning", "s3:PutLifecycleConfiguration", "s3:DeleteBucket", "s3:PutBucketPolicy"],
      "Resource": ["arn:aws:s3:::stepanel-backups-prod", "arn:aws:s3:::stepanel-backups-prod/*"]
    }
  ]
}
```

`s3:DeleteObject` only adds delete markers in a versioned bucket. The panel
uses it for the readiness probe, and it cannot remove a locked version.
`s3:DeleteObjectVersion` is deliberately absent.

Configure the rclone remote with these credentials, for example:

```ini
[s3]
type = s3
provider = AWS
env_auth = true
region = eu-west-1
```

Then set the following in `/etc/ste-panel.env` and re-run the installer:

```sh
STEPANEL_OFFSITE_TARGET=s3:stepanel-backups-prod/stepanel
STEPANEL_OFFSITE_OBJECT_LOCK=1
```

Check `/readyz`: `offsite_backup` (or the target readiness detail) reports
`Object Lock verified` once the first proof completes.

## Other S3-compatible providers

Any provider that implements S3 Object Lock with version listing works the
same way (for example Backblaze B2, Wasabi or MinIO). The readiness proof
decides: if the provider lets the panel's credentials remove a version, the
check fails.

## Verification status

The `Offsite Object Lock` workflow (`.github/workflows/offsite-object-lock.yml`)
runs the real rclone (the Ubuntu 24.04 package, v1.60) against versitygw, an
S3 server that enforces Object Lock, on every change to the offsite code and
weekly. The proof must pass for a compliance-locked bucket and fail for a
versioned bucket without lock and for an unversioned bucket. A locked upload
and an ordinary staged upload must both publish, accept a retry, and restore
a signed backup that passes strict verification. It has not been run against
AWS S3 itself; providers differ in details, and the readiness proof is the
check that matters on a real host.
