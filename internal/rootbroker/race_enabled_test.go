//go:build race

package rootbroker

// raceDetectorEnabled reports whether tests run under -race, whose shadow
// memory makes heap measurements meaningless.
const raceDetectorEnabled = true
