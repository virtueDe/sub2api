package service

import "testing"

func TestStripImageURLQuery(t *testing.T) {
	got := stripImageURLQuery("https://example.com/a.png?X-Amz-Signature=abc#preview")
	if got != "https://example.com/a.png" {
		t.Fatalf("stripImageURLQuery() = %q, want clean path", got)
	}
}

func TestSettingAccountSelected(t *testing.T) {
	if !settingAccountSelected(12, nil) {
		t.Fatal("empty account selection should apply to all accounts")
	}
	if !settingAccountSelected(12, []int64{12, 13}) || settingAccountSelected(11, []int64{12, 13}) {
		t.Fatal("non-empty account selection should restrict to selected accounts")
	}
}
