package drive

import (
	"context"
	"errors"
	"fmt"
)

type CapabilityContractViolation struct {
	Capability Capability
	Operation  string
	Err        error
}

func (v CapabilityContractViolation) Error() string {
	return fmt.Sprintf("drive: %s without %s capability returned %v, want unsupported", v.Operation, v.Capability, v.Err)
}

// CheckUnsupportedCapabilities verifies the negative side of the driver
// contract: operations for capabilities a driver does not advertise must fail
// with an unsupported error. It intentionally does not call advertised
// capabilities because those may perform real backend mutations.
func CheckUnsupportedCapabilities(ctx context.Context, d Driver) []CapabilityContractViolation {
	if d == nil {
		return nil
	}
	var violations []CapabilityContractViolation
	dummyEntry := Entry{ID: "__qrypt_contract_entry__", ParentID: "__qrypt_contract_parent__", Name: "__qrypt_contract_name__"}
	check := func(cap Capability, op string, err error) {
		if isUnsupported(err) {
			return
		}
		violations = append(violations, CapabilityContractViolation{Capability: cap, Operation: op, Err: err})
	}

	if !HasCapability(d, CapabilityWriter) {
		_, err := d.Mkdir(ctx, "__qrypt_contract_parent__", "__qrypt_contract_name__")
		check(CapabilityWriter, "Mkdir", err)
		check(CapabilityWriter, "Move", d.Move(ctx, dummyEntry, "__qrypt_contract_parent__"))
		check(CapabilityWriter, "Rename", d.Rename(ctx, dummyEntry, "__qrypt_contract_renamed__"))
		check(CapabilityWriter, "Remove", d.Remove(ctx, dummyEntry))
	}

	if !HasCapability(d, CapabilitySourceUploader) {
		_, err := d.PutSource(ctx, UploadRequest{
			ParentID: "__qrypt_contract_parent__",
			Name:     "__qrypt_contract_name__",
			Source:   NewBytesReadOnlyFileSource(nil),
		})
		check(CapabilitySourceUploader, "PutSource", err)
	}

	if !HasCapability(d, CapabilityPathResolver) {
		_, err := d.ResolvePath(ctx, "/__qrypt_contract_path__")
		check(CapabilityPathResolver, "ResolvePath", err)
	}

	if !HasCapability(d, CapabilityRemoteNameResolver) {
		_, err := d.ResolveRemoteName(ctx, "__qrypt_contract_name__")
		check(CapabilityRemoteNameResolver, "ResolveRemoteName", err)
	}

	if !HasCapability(d, CapabilityForeignEntries) {
		_, err := d.ForeignEntries(ctx, "__qrypt_contract_parent__")
		check(CapabilityForeignEntries, "ForeignEntries", err)
	}

	if !HasCapability(d, CapabilitySpace) {
		_, err := d.Space(ctx)
		check(CapabilitySpace, "Space", err)
	}

	return violations
}

func isUnsupported(err error) bool {
	return errors.Is(err, ErrUnsupported) || errors.Is(err, ErrSpaceUnsupported)
}
