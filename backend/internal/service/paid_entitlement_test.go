//go:build unit

package service

import (
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
)

func TestIsPaidRedeemAmount(t *testing.T) {
	tests := []struct {
		name  string
		value float64
		want  bool
	}{
		{name: "below minimum", value: 9, want: false},
		{name: "minimum integer", value: 10, want: true},
		{name: "fractional", value: 10.5, want: false},
		{name: "larger integer", value: 20, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPaidRedeemAmount(tt.value); got != tt.want {
				t.Fatalf("isPaidRedeemAmount(%v) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestIsPaidBalanceRedeemCode(t *testing.T) {
	valid := &RedeemCode{
		Type:                RedeemTypeBalance,
		Value:               10,
		EntitlementProfile:  PaidEntitlementProfileDefault,
		EntitlementGroupIDs: []int64{8},
	}
	if !IsPaidBalanceRedeemCode(valid) {
		t.Fatal("expected configured integer balance code to qualify")
	}

	tests := []*RedeemCode{
		nil,
		{Type: RedeemTypeConcurrency, Value: 10, EntitlementProfile: PaidEntitlementProfileDefault, EntitlementGroupIDs: []int64{8}},
		{Type: RedeemTypeBalance, Value: 9, EntitlementProfile: PaidEntitlementProfileDefault, EntitlementGroupIDs: []int64{8}},
		{Type: RedeemTypeBalance, Value: 10.5, EntitlementProfile: PaidEntitlementProfileDefault, EntitlementGroupIDs: []int64{8}},
		{Type: RedeemTypeBalance, Value: 10, EntitlementProfile: "none", EntitlementGroupIDs: []int64{8}},
		{Type: RedeemTypeBalance, Value: 10, EntitlementProfile: PaidEntitlementProfileDefault},
	}
	for i, code := range tests {
		if IsPaidBalanceRedeemCode(code) {
			t.Fatalf("case %d unexpectedly qualified", i)
		}
	}
}

func TestValidatePaidEntitlementTarget(t *testing.T) {
	valid := &dbent.Group{Status: StatusActive, IsExclusive: true, SubscriptionType: SubscriptionTypeStandard}
	if err := validatePaidEntitlementTarget(valid); err != nil {
		t.Fatalf("valid group rejected: %v", err)
	}

	tests := []*dbent.Group{
		{Status: "inactive", IsExclusive: true, SubscriptionType: SubscriptionTypeStandard},
		{Status: StatusActive, IsExclusive: false, SubscriptionType: SubscriptionTypeStandard},
		{Status: StatusActive, IsExclusive: true, SubscriptionType: SubscriptionTypeSubscription},
	}
	for i, group := range tests {
		if err := validatePaidEntitlementTarget(group); err == nil {
			t.Fatalf("case %d unexpectedly passed validation", i)
		}
	}
}

func TestMergeUniqueInt64(t *testing.T) {
	got := mergeUniqueInt64([]int64{8, 13, 0}, []int64{13, 20, -1})
	want := []int64{8, 13, 20}
	if len(got) != len(want) {
		t.Fatalf("merge length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("merge[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}
