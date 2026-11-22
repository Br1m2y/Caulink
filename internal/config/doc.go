// Package config loads caulinkd's configuration.
//
// Owner: A (shared by B and C).
//
// Source order: file (configs/caulinkd.example.json shape), then environment
// overrides. Secrets such as campus credentials never enter the repository;
// they live in the local config or the OS credential store.
//
// TODO: typed structs for pool/auth/scheduler/control sections, validation
// with actionable errors, and a --check flag for CI.
package config
