// Package classify labels a flow so the scheduler knows whether it may be
// spread across source IPs or must be pinned to the best one.
//
// Owner: C.
//
// The first version is a rule engine, deliberately explainable: destination
// port, protocol, host patterns. No ML classifier until the rules are proven
// insufficient.
//
// TODO: FlowClass enum (bulk, interactive, realtime), Classify(flow), rule
// table loaded from configs/caulinkd.example.json.
package classify
