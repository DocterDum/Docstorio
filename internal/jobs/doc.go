// Package jobs provides the busy lock and the job log (DESIGN "Jobs").
//
// One Manager holds at most one busy job at a time. Start, stop, restart,
// world creation and every mod change first call TryBegin (short operations
// answered directly) or Run (install, update and world creation, answered
// with {"jobId"}); a second caller gets *BusyError.
//
// A Job collects numbered log lines (Logf), passes every line through the
// Manager's redaction function (Redact by default, so "token=..." never
// reaches a job line) and ends in state done or failed. Finished jobs can be
// fetched for PruneAfter.
//
// jobs imports nothing from this module, so every package may accept a
// *jobs.Job (or the smaller Logger interface it satisfies).
package jobs
