package runtime_test

import (
	"reflect"
	"testing"

	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/ent/redeemcode"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// Importing runtime must initialize both upstream and fork-specific defaults.
func TestRuntimeDefaults(t *testing.T) {
	tests := []struct {
		name string
		got  any
		want any
	}{
		{"group status", group.DefaultStatus, domain.StatusActive},
		{"paid entitlement", group.DefaultIsPaidEntitlement, false},
		{"messages dispatch", group.DefaultMessagesDispatchModelConfig, domain.OpenAIMessagesDispatchModelConfig{}},
		{"model allowlist", group.DefaultModelAllowlist, domain.GroupModelAllowlist{}},
		{"codex manifest", group.DefaultCodexModelsManifestConfig, domain.GroupCodexModelsManifestConfig{}},
		{"redeem entitlement groups", redeemcode.DefaultEntitlementGroupIds, []int64{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.got, tt.want) {
				t.Fatalf("default = %#v, want %#v", tt.got, tt.want)
			}
		})
	}
}
