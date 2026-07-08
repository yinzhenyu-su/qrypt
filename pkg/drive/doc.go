// Package drive defines qrypt's provider-facing backend contract.
//
// The base Driver interface is intentionally small. Optional behavior is
// exposed through focused interfaces and reported with Capabilities so higher
// layers can make runtime decisions without importing concrete providers.
package drive
