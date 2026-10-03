package main

import "time"

// orphanedStagingMinAge is how long a staging tree must be untouched before
// cleanup treats it as abandoned. It exceeds the longest operation timeout
// (helper.BackupRestoreTimeout, 120 minutes), so a tree still in use by a
// live operation, including one in an external worker process, is never
// removed.
const orphanedStagingMinAge = 3 * time.Hour
