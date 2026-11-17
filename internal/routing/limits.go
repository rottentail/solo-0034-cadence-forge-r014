// Package routing holds the constants that define how the independent sound
// paths are measured for a two-channel review. The measurement and the record
// kept with the plan must agree, so both read them from here.
package routing

// AnalysisRate is the sample rate the routing measurement uses. It is an
// analysis rate, never the delivery rate.
const AnalysisRate = 16000

// AnalysisWindowMS bounds how much of the assembled composition is measured,
// so analysis cost does not grow with a long sheet.
const AnalysisWindowMS = 30000

// WarmupMS discards correlator start-up before phase is read.
const WarmupMS = 200
