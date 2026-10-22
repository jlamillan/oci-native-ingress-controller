package nodeBackend

import (
	"testing"

	"github.com/oracle/oci-go-sdk/v65/common"
	ociloadbalancer "github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/oracle/oci-native-ingress-controller/pkg/util"
)

func TestPlanBackendRepairOnlyRemovesAbsentNodeIPsFromManagedSets(t *testing.T) {
	backend := func(ip string) ociloadbalancer.Backend {
		return ociloadbalancer.Backend{IpAddress: common.String(ip), Port: common.Int(30000)}
	}
	lb := &ociloadbalancer.LoadBalancer{BackendSets: map[string]ociloadbalancer.BackendSet{
		"managed":                  {Backends: []ociloadbalancer.Backend{backend("10.0.0.1"), backend("10.0.0.2"), backend("10.0.0.3")}},
		"unmanaged":                {Backends: []ociloadbalancer.Backend{backend("10.0.0.2")}},
		util.DefaultBackendSetName: {Backends: []ociloadbalancer.Backend{backend("10.0.0.2")}},
	}}
	plan, err := planBackendRepair(lb, map[string]bool{"managed": true}, map[string]bool{"10.0.0.1": true}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if plan.currentTotal != 5 || plan.afterPrune != 3 || plan.afterDefault != 4 {
		t.Fatalf("unexpected totals: %+v", plan)
	}
	if len(plan.stale) != 2 || plan.stale[0].name != "10.0.0.2:30000" || plan.stale[1].name != "10.0.0.3:30000" {
		t.Fatalf("unexpected stale registrations: %+v", plan.stale)
	}
}

func TestPlanBackendRepairPreservesLastBackendAndRejectsOverLimitUpdate(t *testing.T) {
	backend := func(ip string) ociloadbalancer.Backend {
		return ociloadbalancer.Backend{IpAddress: common.String(ip), Port: common.Int(30000)}
	}
	lb := &ociloadbalancer.LoadBalancer{BackendSets: map[string]ociloadbalancer.BackendSet{
		"managed":                  {Backends: []ociloadbalancer.Backend{backend("10.0.0.2")}},
		util.DefaultBackendSetName: {Backends: []ociloadbalancer.Backend{backend("10.0.0.3")}},
	}}
	plan, err := planBackendRepair(lb, map[string]bool{"managed": true}, map[string]bool{"10.0.0.1": true}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.stale) != 0 {
		t.Fatalf("last backend would be deleted: %+v", plan.stale)
	}
	if canUpdateDefault(513, 1, 1) || canUpdateDefault(512, 1, 2) || !canUpdateDefault(512, 2, 2) {
		t.Fatal("default update capacity gate is incorrect")
	}
}

func TestPlanBackendRepairSkipsMissingSetsWithoutDefault(t *testing.T) {
	lb := &ociloadbalancer.LoadBalancer{BackendSets: map[string]ociloadbalancer.BackendSet{
		"managed": {Backends: []ociloadbalancer.Backend{
			{IpAddress: common.String("10.0.0.1"), Port: common.Int(30000)},
			{IpAddress: common.String("10.0.0.2"), Port: common.Int(30000)},
		}},
	}}
	plan, err := planBackendRepair(lb, map[string]bool{"managed": true, "not-yet-created": true}, map[string]bool{"10.0.0.1": true}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if plan.defaultPresent || len(plan.missingSets) != 1 || plan.missingSets[0] != "not-yet-created" {
		t.Fatalf("unexpected incomplete-LB plan: %+v", plan)
	}
	if len(plan.stale) != 1 || plan.stale[0].name != "10.0.0.2:30000" || plan.afterPrune != 1 {
		t.Fatalf("stale regular backend was not planned: %+v", plan)
	}
}

func TestRepairHealthPriority(t *testing.T) {
	for _, test := range []struct {
		name     string
		health   *ociloadbalancer.LoadBalancerHealth
		priority int
	}{
		{name: "unknown", health: nil, priority: 0},
		{name: "warning", health: &ociloadbalancer.LoadBalancerHealth{WarningStateBackendSetNames: []string{"regular"}}, priority: 1},
		{name: "regular critical", health: &ociloadbalancer.LoadBalancerHealth{CriticalStateBackendSetNames: []string{"regular"}}, priority: 2},
		{name: "default critical", health: &ociloadbalancer.LoadBalancerHealth{CriticalStateBackendSetNames: []string{"regular", util.DefaultBackendSetName}}, priority: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := repairHealthPriority(test.health); got != test.priority {
				t.Fatalf("priority = %d, want %d", got, test.priority)
			}
		})
	}
}
