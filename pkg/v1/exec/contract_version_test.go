package exec_test

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
)

// TestContractVersionIsTheOneTheGoldenFreezes ties exec.ContractVersion to the reviewed listing of the contract:
// the first line of proto/exec/v2/exec.contract.golden is "contract_version N" and N is the version of the contract
// that the listing below it freezes. Raising the contract version raises that header (and, with it, reviews the
// listing); a version that the code and the golden disagree on fails here.
func TestContractVersionIsTheOneTheGoldenFreezes(t *testing.T) {
	raw, err := os.ReadFile("../../../proto/exec/v2/exec.contract.golden")
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := strings.Cut(string(raw), "\n")
	first = strings.TrimSpace(first)
	number, ok := strings.CutPrefix(first, "contract_version ")
	if !ok {
		t.Fatalf("the first line of exec.contract.golden is %q, want \"contract_version <N>\"", first)
	}
	version, err := strconv.ParseUint(number, 10, 32)
	if err != nil {
		t.Fatalf("the contract_version in %q is not a number: %v", first, err)
	}
	if uint32(version) != exec.ContractVersion {
		t.Fatalf("exec.ContractVersion is %d and exec.contract.golden freezes contract_version %d: "+
			"a released contract never changes, so raise both together and review the golden's diff",
			exec.ContractVersion, version)
	}
}
