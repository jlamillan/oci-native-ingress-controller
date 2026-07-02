package logging

import (
	"context"
	"testing"

	"github.com/oracle/oci-go-sdk/v65/common"
	ocilogging "github.com/oracle/oci-go-sdk/v65/logging"
	"github.com/oracle/oci-native-ingress-controller/pkg/exception"
	"github.com/oracle/oci-native-ingress-controller/pkg/util"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
)

func TestEnsureLoadBalancerLogsCreatesRequestedLogs(t *testing.T) {
	ic := ingressClassWithAnnotations(map[string]string{
		util.IngressClassAccessLogGroupIdAnnotation: "access-group",
		util.IngressClassErrorLogGroupIdAnnotation:  "error-group",
	})
	fakeLogging := newMockLoggingClient()
	c := Client{LoggingClient: fakeLogging}

	err := c.EnsureLoadBalancerLogs(context.Background(), fakeclientset.NewSimpleClientset(ic), ic, "lb-id")
	if err != nil {
		t.Fatalf("EnsureLoadBalancerLogs() error = %v", err)
	}

	if len(fakeLogging.createRequests) != 2 {
		t.Fatalf("expected 2 create requests, got %d", len(fakeLogging.createRequests))
	}
	assertLogExists(t, fakeLogging, "access-group", util.LoadBalancerAccessLogCategory, true)
	assertLogExists(t, fakeLogging, "error-group", util.LoadBalancerErrorLogCategory, true)
}

func TestEnsureLoadBalancerLogsEnablesExistingLog(t *testing.T) {
	ic := ingressClassWithAnnotations(map[string]string{
		util.IngressClassAccessLogGroupIdAnnotation: "access-group",
	})
	fakeLogging := newMockLoggingClient()
	fakeLogging.addLog("access-group", "access-log", "lb-id", util.LoadBalancerAccessLogCategory, false)
	c := Client{LoggingClient: fakeLogging}

	err := c.EnsureLoadBalancerLogs(context.Background(), fakeclientset.NewSimpleClientset(ic), ic, "lb-id")
	if err != nil {
		t.Fatalf("EnsureLoadBalancerLogs() error = %v", err)
	}

	if len(fakeLogging.createRequests) != 0 {
		t.Fatalf("expected no create requests, got %d", len(fakeLogging.createRequests))
	}
	assertLogExists(t, fakeLogging, "access-group", util.LoadBalancerAccessLogCategory, true)
}

func TestEnsureLoadBalancerLogsDisablesManagedLogWhenAnnotationRemoved(t *testing.T) {
	ic := ingressClassWithAnnotations(map[string]string{
		util.IngressClassAccessLogIdAnnotation:             "access-log",
		util.IngressClassAccessLogAppliedGroupIdAnnotation: "access-group",
	})
	fakeLogging := newMockLoggingClient()
	fakeLogging.addLog("access-group", "access-log", "lb-id", util.LoadBalancerAccessLogCategory, true)
	c := Client{LoggingClient: fakeLogging}

	err := c.EnsureLoadBalancerLogs(context.Background(), fakeclientset.NewSimpleClientset(ic), ic, "lb-id")
	if err != nil {
		t.Fatalf("EnsureLoadBalancerLogs() error = %v", err)
	}

	assertLogExists(t, fakeLogging, "access-group", util.LoadBalancerAccessLogCategory, false)
}

func TestEnsureLoadBalancerLogsRemovesManagedAnnotationsWhenDisabled(t *testing.T) {
	ic := ingressClassWithAnnotations(map[string]string{
		util.IngressClassAccessLogIdAnnotation:             "access-log",
		util.IngressClassAccessLogAppliedGroupIdAnnotation: "access-group",
	})
	fakeLogging := newMockLoggingClient()
	fakeLogging.addLog("access-group", "access-log", "lb-id", util.LoadBalancerAccessLogCategory, true)
	c := Client{LoggingClient: fakeLogging}
	kubeClient := fakeclientset.NewSimpleClientset(ic)

	err := c.EnsureLoadBalancerLogs(context.Background(), kubeClient, ic, "lb-id")
	if err != nil {
		t.Fatalf("EnsureLoadBalancerLogs() error = %v", err)
	}

	updated, err := kubeClient.NetworkingV1().IngressClasses().Get(context.Background(), ic.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get IngressClass error = %v", err)
	}
	if _, ok := updated.Annotations[util.IngressClassAccessLogIdAnnotation]; ok {
		t.Fatalf("expected %s annotation to be removed", util.IngressClassAccessLogIdAnnotation)
	}
	if _, ok := updated.Annotations[util.IngressClassAccessLogAppliedGroupIdAnnotation]; ok {
		t.Fatalf("expected %s annotation to be removed", util.IngressClassAccessLogAppliedGroupIdAnnotation)
	}
}

func ingressClassWithAnnotations(annotations map[string]string) *networkingv1.IngressClass {
	return &networkingv1.IngressClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-class",
			Annotations: annotations,
		},
		Spec: networkingv1.IngressClassSpec{
			Controller: "oci.oraclecloud.com/native-ingress-controller",
		},
	}
}

func assertLogExists(t *testing.T, fakeLogging *mockLoggingClient, logGroupID string, category string, enabled bool) {
	t.Helper()
	for _, log := range fakeLogging.logs[logGroupID] {
		if isLoadBalancerLog(log.Configuration, "lb-id", category) {
			if log.IsEnabled == nil || *log.IsEnabled != enabled {
				t.Fatalf("log %s enabled = %v, want %v", category, log.IsEnabled, enabled)
			}
			return
		}
	}
	t.Fatalf("expected %s log in group %s", category, logGroupID)
}

type mockLoggingClient struct {
	logs           map[string]map[string]*ocilogging.Log
	createRequests []ocilogging.CreateLogRequest
	updateRequests []ocilogging.UpdateLogRequest
}

func newMockLoggingClient() *mockLoggingClient {
	return &mockLoggingClient{
		logs: map[string]map[string]*ocilogging.Log{},
	}
}

func (m *mockLoggingClient) addLog(logGroupID string, logID string, lbID string, category string, enabled bool) {
	if m.logs[logGroupID] == nil {
		m.logs[logGroupID] = map[string]*ocilogging.Log{}
	}
	m.logs[logGroupID][logID] = &ocilogging.Log{
		Id:         common.String(logID),
		LogGroupId: common.String(logGroupID),
		IsEnabled:  common.Bool(enabled),
		Configuration: &ocilogging.Configuration{
			Source: ocilogging.OciService{
				Service:  common.String(loadBalancerServiceName),
				Resource: common.String(lbID),
				Category: common.String(category),
			},
		},
	}
}

func (m *mockLoggingClient) CreateLog(ctx context.Context, request ocilogging.CreateLogRequest) (ocilogging.CreateLogResponse, error) {
	m.createRequests = append(m.createRequests, request)
	logID := *request.CreateLogDetails.Configuration.Source.(ocilogging.OciService).Category + "-log"
	source := request.CreateLogDetails.Configuration.Source.(ocilogging.OciService)
	m.addLog(*request.LogGroupId, logID, *source.Resource, *source.Category, *request.CreateLogDetails.IsEnabled)
	return ocilogging.CreateLogResponse{}, nil
}

func (m *mockLoggingClient) GetLog(ctx context.Context, request ocilogging.GetLogRequest) (ocilogging.GetLogResponse, error) {
	if group := m.logs[*request.LogGroupId]; group != nil {
		if log := group[*request.LogId]; log != nil {
			return ocilogging.GetLogResponse{
				Log:  *log,
				Etag: common.String("etag"),
			}, nil
		}
	}
	return ocilogging.GetLogResponse{}, &exception.NotFoundServiceError{}
}

func (m *mockLoggingClient) GetWorkRequest(ctx context.Context, request ocilogging.GetWorkRequestRequest) (ocilogging.GetWorkRequestResponse, error) {
	return ocilogging.GetWorkRequestResponse{}, nil
}

func (m *mockLoggingClient) ListLogs(ctx context.Context, request ocilogging.ListLogsRequest) (ocilogging.ListLogsResponse, error) {
	var items []ocilogging.LogSummary
	for _, log := range m.logs[*request.LogGroupId] {
		items = append(items, ocilogging.LogSummary{
			Id:            log.Id,
			LogGroupId:    log.LogGroupId,
			IsEnabled:     log.IsEnabled,
			Configuration: log.Configuration,
			LogType:       ocilogging.LogSummaryLogTypeService,
		})
	}
	return ocilogging.ListLogsResponse{Items: items}, nil
}

func (m *mockLoggingClient) UpdateLog(ctx context.Context, request ocilogging.UpdateLogRequest) (ocilogging.UpdateLogResponse, error) {
	m.updateRequests = append(m.updateRequests, request)
	if group := m.logs[*request.LogGroupId]; group != nil {
		if log := group[*request.LogId]; log != nil {
			log.IsEnabled = request.IsEnabled
		}
	}
	return ocilogging.UpdateLogResponse{}, nil
}
