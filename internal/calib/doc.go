// Package calib is the self-adjusting calibration of `wy account next`'s lanes strategy: the
// used readings of a seat, the pairs between them, the meter factor, the ratio and the lane
// price measured from them and blended with the fallbacks, the restarts after one-sided misses,
// and the meter margins of the online corrector. It is pure: the hub's calibration job feeds it
// incrementally from the store, and the picker feeds it from what it read.
//
// Contract: docs/spec/setup.md 8.5 (Calibration), docs/spec/api.yaml Calibration.
//
// Depends on: the standard library only.
package calib
