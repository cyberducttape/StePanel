package stepanel

import "time"

// orphanedStagingMinAge is a secondary safeguard for legacy or unowned scratch
// trees. Current restore/import stages are protected by cross-process advisory
// ownership locks; age alone is not evidence that a stage is abandoned.
const orphanedStagingMinAge = 3 * time.Hour
