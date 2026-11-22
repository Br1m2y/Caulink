// Package logx provides structured logging for every module.
//
// Owner: A (shared by B and C).
//
// One logger shape for the daemon and its children so Phase 0 measurements
// and Phase 2 failure analysis can be read by the same tooling. Log lines are
// line-delimited JSON; human-readable text is a --verbose rendering, not a
// separate path.
//
// TODO: New(level, writer), fields helper, and a rotating file sink under
// .local/logs (ignored by git).
package logx
