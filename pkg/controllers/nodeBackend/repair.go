package nodeBackend

import (
	"context"
	"fmt"
	"sort"
	"time"

	ociloadbalancer "github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/oracle/oci-native-ingress-controller/pkg/client"
	"github.com/oracle/oci-native-ingress-controller/pkg/util"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"
)

const maxLoadBalancerBackends = 512

type repairTarget struct {
	class    *networkingv1.IngressClass
	lbID     string
	wrapper  *client.WrapperClient
	priority int
}

type staleBackend struct {
	set  string
	name string
	ip   string
}

type backendRepairPlan struct {
	stale          []staleBackend
	currentTotal   int
	afterPrune     int
	afterDefault   int
	currentDefault int
	defaultPresent bool
	missingSets    []string
}

// RunRepair makes one bounded pass over load balancers referenced by managed IngressClasses.
// Without apply, it only logs the plan. Normal controllers are not started in this mode.
func (c *Controller) RunRepair(ctx context.Context, apply bool) {
	classes, err := c.ingressClassLister.List(labels.Everything())
	if err != nil {
		klog.ErrorS(err, "Backend repair could not list ingress classes")
		return
	}
	targets := make([]repairTarget, 0, len(classes))
	seen := map[string]bool{}
	for _, class := range classes {
		if class.Spec.Controller != c.controllerClass {
			continue
		}
		lbID := util.GetIngressClassLoadBalancerId(class)
		if lbID == "" || seen[lbID] {
			continue
		}
		seen[lbID] = true
		clientCtx, err := client.GetClientContext(class, c.saLister, c.client, "", class.Name)
		if err != nil {
			klog.ErrorS(err, "Backend repair could not create OCI client", "ingressClass", class.Name)
			return
		}
		wrapper, ok := clientCtx.Value(util.WrapperClient).(*client.WrapperClient)
		if !ok {
			klog.ErrorS(fmt.Errorf("OCI client missing"), "Backend repair stopped", "ingressClass", class.Name)
			return
		}
		priority := 0
		health, err := wrapper.GetLbClient().GetLoadBalancerHealth(ctx, lbID)
		if err != nil {
			if util.IsServiceError(err, 429) {
				klog.ErrorS(err, "Backend repair stopped after OCI rate limit", "lbID", lbID)
				return
			}
			klog.ErrorS(err, "Backend repair could not read LB health; using normal priority", "lbID", lbID)
		} else {
			priority = repairHealthPriority(health)
		}
		targets = append(targets, repairTarget{class: class, lbID: lbID, wrapper: wrapper, priority: priority})
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
	klog.InfoS("Backend repair discovered managed load balancers", "count", len(targets), "apply", apply)
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].priority != targets[j].priority {
			return targets[i].priority > targets[j].priority
		}
		return targets[i].lbID < targets[j].lbID
	})
	for _, target := range targets {
		if ctx.Err() != nil {
			return
		}
		if err := c.repairLoadBalancer(ctx, target, apply); err != nil {
			if apply || util.IsServiceError(err, 429) {
				klog.ErrorS(err, "Backend repair stopped", "lbID", target.lbID)
				return // Do not fan out after an OCI mutation error, especially a tenant 429.
			}
			klog.ErrorS(err, "Backend repair could not plan LB; continuing inventory", "lbID", target.lbID)
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
	klog.InfoS("Backend repair pass completed", "loadBalancers", len(targets), "apply", apply)
}

func (c *Controller) repairLoadBalancer(ctx context.Context, target repairTarget, apply bool) error {
	ingresses, err := util.GetIngressesForClass(c.ingressLister, target.class)
	if err != nil {
		return err
	}
	managedSets, err := c.managedBackendSets(ingresses)
	if err != nil {
		return err
	}
	defaultBackends, err := c.getDefaultBackends(ingresses)
	if err != nil {
		klog.ErrorS(err, "Default backends could not be planned; continuing with stale regular-backend cleanup", "lbID", target.lbID)
		defaultBackends = nil
	}
	if len(managedSets) == 0 && len(defaultBackends) == 0 {
		klog.V(2).InfoS("No managed backend sets or desired default endpoints; skipping LB", "lbID", target.lbID)
		return nil
	}
	seenDefault := map[string]bool{}
	for _, backend := range defaultBackends {
		if backend.IpAddress == nil || *backend.IpAddress == "" || backend.Port == nil || *backend.Port <= 0 {
			return fmt.Errorf("invalid desired default backend; refusing repair")
		}
		name := fmt.Sprintf("%s:%d", *backend.IpAddress, *backend.Port)
		if seenDefault[name] {
			return fmt.Errorf("duplicate desired default backend %s; refusing repair", name)
		}
		seenDefault[name] = true
	}
	nodeIPs, err := c.allNodeIPs()
	if err != nil {
		return err
	}
	lb, _, err := target.wrapper.GetLbClient().RefreshLoadBalancer(ctx, target.lbID)
	if err != nil {
		return err
	}
	plan, err := planBackendRepair(lb, managedSets, nodeIPs, len(defaultBackends))
	if err != nil {
		return err
	}
	klog.InfoS("Backend repair plan", "lbID", target.lbID, "ingressClass", target.class.Name,
		"currentTotal", plan.currentTotal, "staleRemovable", len(plan.stale), "afterPrune", plan.afterPrune,
		"afterDefault", plan.afterDefault, "defaultUpdateFits", plan.defaultPresent && len(defaultBackends) > 0 && canUpdateDefault(plan.afterPrune, plan.currentDefault, len(defaultBackends)),
		"missingManagedSets", plan.missingSets,
		"healthPriority", target.priority, "apply", apply)
	for _, backend := range plan.stale {
		klog.InfoS("Backend repair stale registration", "lbID", target.lbID, "backendSet", backend.set, "backend", backend.name, "apply", apply)
	}
	if len(plan.stale) == 0 && target.priority < 3 {
		klog.V(2).InfoS("No stale registrations or critical default set; skipping LB", "lbID", target.lbID)
		return nil
	}
	if !apply {
		return nil
	}
	for _, backend := range plan.stale {
		currentIPs, err := c.allNodeIPs()
		if err != nil {
			return err
		}
		if currentIPs[backend.ip] {
			return fmt.Errorf("node IP %s reappeared; stop before deleting %s", backend.ip, backend.name)
		}
		deleted, err := target.wrapper.GetLbClient().DeleteBackendIfPresent(ctx, target.lbID, backend.set, backend.name)
		if err != nil {
			return err
		}
		klog.InfoS("Backend repair deletion completed", "lbID", target.lbID, "backendSet", backend.set, "backend", backend.name, "deleted", deleted)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	lb, _, err = target.wrapper.GetLbClient().RefreshLoadBalancer(ctx, target.lbID)
	if err != nil {
		return err
	}
	currentTotal := countLoadBalancerBackends(lb)
	defaultSet, ok := lb.BackendSets[util.DefaultBackendSetName]
	if !ok || len(defaultBackends) == 0 {
		klog.InfoS("Skipping default backend update without a set and desired endpoints", "lbID", target.lbID,
			"defaultSetPresent", ok, "desiredBackends", len(defaultBackends))
		return nil
	}
	if !canUpdateDefault(currentTotal, len(defaultSet.Backends), len(defaultBackends)) {
		klog.InfoS("Skipping default backend update until the LB-wide count fits", "lbID", target.lbID,
			"currentTotal", currentTotal, "projectedTotal", currentTotal-len(defaultSet.Backends)+len(defaultBackends))
		return nil
	}
	return target.wrapper.GetLbClient().UpdateBackends(ctx, target.lbID, util.DefaultBackendSetName, defaultBackends)
}

func repairHealthPriority(health *ociloadbalancer.LoadBalancerHealth) int {
	if health == nil {
		return 0
	}
	for _, set := range health.CriticalStateBackendSetNames {
		if set == util.DefaultBackendSetName {
			return 3
		}
	}
	if len(health.CriticalStateBackendSetNames) > 0 {
		return 2
	}
	if len(health.WarningStateBackendSetNames) > 0 {
		return 1
	}
	return 0
}

func canUpdateDefault(currentTotal, currentDefault, desiredDefault int) bool {
	return currentTotal <= maxLoadBalancerBackends && currentTotal-currentDefault+desiredDefault <= maxLoadBalancerBackends
}

func (c *Controller) allNodeIPs() (map[string]bool, error) {
	nodes, err := c.nodeLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("node informer is empty; refusing repair")
	}
	ips := map[string]bool{}
	for _, node := range nodes {
		if ip := NodeInternalIP(node); ip != "" {
			ips[ip] = true
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no Kubernetes node InternalIPs; refusing repair")
	}
	return ips, nil
}

func (c *Controller) managedBackendSets(ingresses []*networkingv1.Ingress) (map[string]bool, error) {
	sets := map[string]bool{}
	for _, ingress := range ingresses {
		for _, rule := range ingress.Spec.Rules {
			if !util.HasHTTPPaths(rule) {
				continue
			}
			for _, path := range rule.HTTP.Paths {
				if !util.HasServiceBackend(path) {
					continue
				}
				pSvc, svc, err := util.ExtractServices(path, c.serviceLister, ingress)
				if err != nil {
					klog.ErrorS(err, "Skipping ingress path while finding managed backend sets", "ingress", klog.KObj(ingress))
					continue
				}
				svcName, svcPort, nodePort, err := util.PathToServiceAndTargetPort(c.endpointLister, svc, pSvc, ingress.Namespace, true)
				if err != nil {
					klog.ErrorS(err, "Skipping ingress path while finding managed backend sets", "ingress", klog.KObj(ingress))
					continue
				}
				if svc != nil && nodePort != 0 {
					sets[util.GenerateBackendSetName(ingress.Namespace, svcName, svcPort)] = true
				}
			}
		}
	}
	return sets, nil
}

func countLoadBalancerBackends(lb *ociloadbalancer.LoadBalancer) int {
	total := 0
	for _, set := range lb.BackendSets {
		total += len(set.Backends)
	}
	return total
}

func planBackendRepair(lb *ociloadbalancer.LoadBalancer, managedSets, nodeIPs map[string]bool, defaultCount int) (backendRepairPlan, error) {
	plan := backendRepairPlan{currentTotal: countLoadBalancerBackends(lb)}
	defaultSet, ok := lb.BackendSets[util.DefaultBackendSetName]
	if ok {
		plan.defaultPresent = true
		plan.currentDefault = len(defaultSet.Backends)
	}
	setNames := make([]string, 0, len(managedSets))
	for name := range managedSets {
		setNames = append(setNames, name)
	}
	sort.Strings(setNames)
	for _, name := range setNames {
		set, ok := lb.BackendSets[name]
		if !ok {
			plan.missingSets = append(plan.missingSets, name)
			continue
		}
		remaining := len(set.Backends)
		for _, backend := range set.Backends {
			if backend.IpAddress == nil || backend.Port == nil {
				return plan, fmt.Errorf("backend in set %s is missing IP or port", name)
			}
			if nodeIPs[*backend.IpAddress] || remaining <= 1 {
				continue
			}
			plan.stale = append(plan.stale, staleBackend{set: name, name: fmt.Sprintf("%s:%d", *backend.IpAddress, *backend.Port), ip: *backend.IpAddress})
			remaining--
		}
	}
	plan.afterPrune = plan.currentTotal - len(plan.stale)
	plan.afterDefault = plan.afterPrune
	if plan.defaultPresent && defaultCount > 0 {
		plan.afterDefault = plan.afterPrune - plan.currentDefault + defaultCount
	}
	return plan, nil
}
